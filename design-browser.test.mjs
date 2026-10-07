import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as wait } from "node:timers/promises";
import test from "node:test";

const preview = process.env.TEXTPOD_PREVIEW_URL || "http://127.0.0.1:3030/notes/";
const debug = process.env.CHROMIUM_DEBUG_URL || "http://127.0.0.1:9224";

async function browser() {
    const target = await (await fetch(debug + "/json/new?about:blank", { method: "PUT" })).json();
    const socket = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
        socket.addEventListener("open", resolve, { once: true });
        socket.addEventListener("error", reject, { once: true });
    });
    let id = 0;
    const pending = new Map();
    socket.addEventListener("message", event => {
        const reply = JSON.parse(event.data);
        const request = pending.get(reply.id);
        if (!request) return;
        pending.delete(reply.id);
        clearTimeout(request.timer);
        if (reply.error) request.reject(new Error(reply.error.message));
        else request.resolve(reply.result);
    });
    function send(method, params = {}) {
        return new Promise((resolve, reject) => {
            const requestId = ++id;
            const timer = setTimeout(() => {
                pending.delete(requestId);
                reject(new Error(`${method} timed out`));
            }, 30000);
            pending.set(requestId, { resolve, reject, timer });
            socket.send(JSON.stringify({ id: requestId, method, params }));
        });
    }
    async function evaluate(expression) {
        const reply = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
        if (reply.exceptionDetails) throw new Error(reply.exceptionDetails.text);
        return reply.result.value;
    }
    async function navigate(url) {
        await send("Page.navigate", { url });
        for (let i = 0; i < 300; i++) {
            try {
                if (await evaluate(`location.href === ${JSON.stringify(url)} && document.readyState === "complete"`)) {
                    await evaluate("document.fonts.ready.then(() => true)");
                    return;
                }
            } catch (error) {
                if (!/context/i.test(error.message)) throw error;
            }
            await wait(100);
        }
        throw new Error(`Page did not finish loading: ${url}`);
    }
    async function screenshot(name) {
        if (!process.env.TEXTPOD_SCREENSHOT_DIR) return;
        const { data } = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
        await writeFile(join(process.env.TEXTPOD_SCREENSHOT_DIR, name + ".png"), Buffer.from(data, "base64"));
    }
    await send("Page.enable");
    return { send, evaluate, navigate, screenshot, close: async () => {
        socket.close();
        await fetch(debug + "/json/close/" + target.id);
    } };
}

test("local blog works across themes, viewports and navigation", async t => {
    const page = await browser();
    t.after(() => page.close());
    await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });
    await page.navigate(preview);

    await t.test("desktop homepage and accessible search", async () => {
        assert.equal(await page.evaluate('getComputedStyle(document.querySelector(".hero")).backgroundColor'), "rgb(238, 255, 0)");
        assert.equal(await page.evaluate('document.querySelector("label[for=note-search]").control === document.querySelector("#note-search")'), true);
        assert.equal(await page.evaluate('[...document.fonts].some(font => font.family === "Satoshi" && font.status === "loaded")'), true);
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-desktop-light");
        await page.evaluate('document.querySelector("#notes").scrollIntoView({behavior: "instant"})');
        assert.equal(await page.evaluate('document.querySelector(".site-header").getBoundingClientRect().bottom <= 0'), true);
        await page.screenshot("textpod-notes-light");
        await page.evaluate("window.scrollTo({top: 0, behavior: 'instant'})");
    });

    await t.test("scanned paper and worn ink load from the blog itself", async () => {
        const textures = await page.evaluate(String.raw`(async () => {
            const imageURL = value => new URL(value.match(/url\("([^"]+)"\)/)[1], location.href);
            const paperURL = imageURL(getComputedStyle(document.querySelector('.hero')).backgroundImage);
            const darkURL = imageURL(getComputedStyle(document.documentElement).getPropertyValue('--paper-dark'));
            const inkURL = imageURL(getComputedStyle(document.querySelector('.hero h1')).maskImage);
            const load = async url => {
                const image = new Image();
                image.src = url.href;
                await image.decode();
                return image;
            };
            const [paper, dark, ink] = await Promise.all([load(paperURL), load(darkURL), load(inkURL)]);
            const canvas = new OffscreenCanvas(ink.width, ink.height);
            const context = canvas.getContext('2d');
            context.drawImage(ink, 0, 0);
            const pixels = context.getImageData(0, 0, ink.width, ink.height).data;
            let minAlpha = 255, maxAlpha = 0;
            for (let i = 3; i < pixels.length; i += 4) {
                minAlpha = Math.min(minAlpha, pixels[i]);
                maxAlpha = Math.max(maxAlpha, pixels[i]);
            }
            return {
                sameOrigin: [paperURL, darkURL, inkURL].every(url => url.origin === location.origin),
                paperWidth: paper.width, darkWidth: dark.width, minAlpha, maxAlpha,
            };
        })()`);
        assert.equal(textures.sameOrigin, true);
        assert.ok(textures.paperWidth >= 1400 && textures.darkWidth >= 1400);
        assert.ok(textures.minAlpha < 255);
        assert.equal(textures.maxAlpha, 255);
    });

    await t.test("mobile light and dark layouts stay within the viewport", async () => {
        await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-mobile-light");
        await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "dark" }] });
        assert.equal(await page.evaluate('getComputedStyle(document.body).backgroundColor'), "rgb(20, 20, 19)");
        assert.match(await page.evaluate('getComputedStyle(document.body).backgroundImage'), /paper-305-dark-tile\.jpg/);
        assert.match(await page.evaluate('getComputedStyle(document.querySelector(".hero")).backgroundImage'), /paper-305-tile\.jpg/);
        assert.equal(await page.evaluate('getComputedStyle(document.querySelector(".hero")).color'), "rgb(17, 17, 17)");
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-mobile-dark");
    });

    const noteURL = await page.evaluate('document.querySelector("#notes .subtitle a").href');
    const articles = await (await fetch(new URL("notes", preview))).json();
    const longArticle = articles.find(article => (article.html.match(/<h[1-6]\b/g) || []).length > 2 && article.html.includes("<pre"));
    assert.ok(longArticle, "archive contains an article with headings and code");
    const longNoteURL = new URL("note/" + encodeURIComponent(longArticle.id), preview).href;
    await t.test("individual note navigation and contents", async () => {
        await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
        await page.navigate(noteURL);
        assert.equal(await page.evaluate('document.querySelector(".site-brand").href'), preview);
        assert.equal(await page.evaluate('document.querySelector("#notes #noteView") !== null'), true);
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-note-dark");
        await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-note-mobile-dark");
    });

    await t.test("long articles keep their contents index and expanded code within the viewport", async () => {
        await page.navigate(longNoteURL);
        assert.equal(await page.evaluate('document.querySelector(".note-toc") !== null'), true);
        assert.equal(await page.evaluate('document.querySelector(".note-toc").open'), false);
        await page.evaluate('document.querySelectorAll("details:not(.note-toc)").forEach(detail => detail.open = true)');
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-long-note-mobile-dark");
        await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
        await page.evaluate("new Promise(resolve => requestAnimationFrame(resolve))");
        assert.equal(await page.evaluate('document.querySelector(".note-toc").open'), true);
        assert.equal(await page.evaluate('document.querySelector(".note-toc").getBoundingClientRect().right < document.querySelector(".note-content").getBoundingClientRect().left'), true);
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-long-note-desktop-dark");
    });

    await t.test("quoted notes keep later headings and definition lists in the article column", async () => {
        await page.navigate(new URL("note/20260824091055", preview).href);
        const checkLayout = async () => {
            const layout = await page.evaluate(`(() => {
                const heading = document.querySelector('h2');
                const content = document.querySelector('#noteView .note-content');
                const list = document.querySelector('dl.org-dl');
                const term = list.querySelector('dt');
                const value = list.querySelector('dd');
                return {
                    insideNote: Boolean(heading.closest('#noteView') && list.closest('#noteView')),
                    inContentColumn: Boolean(content && Math.abs(heading.getBoundingClientRect().left - content.getBoundingClientRect().left) < 1),
                    compactHeadingTags: Boolean(content && heading.querySelector('.tags').getBoundingClientRect().width < content.getBoundingClientRect().width / 3),
                    listLayout: getComputedStyle(list).display,
                    distinctColumns: value.getBoundingClientRect().left > term.getBoundingClientRect().right,
                    alignedRow: Math.abs(term.getBoundingClientRect().top - value.getBoundingClientRect().top) < 1,
                    ruledRows: getComputedStyle(value).borderBottomStyle === 'solid',
                };
            })()`);
            assert.equal(layout.insideNote, true);
            assert.equal(layout.inContentColumn, true);
            assert.equal(layout.compactHeadingTags, true);
            assert.equal(layout.listLayout, "grid");
            assert.equal(layout.distinctColumns, true);
            assert.equal(layout.alignedRow, true);
            assert.equal(layout.ruledRows, true);
            assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        };
        await checkLayout();
        await page.evaluate('document.querySelector("h2").scrollIntoView({behavior: "instant"})');
        await page.screenshot("textpod-quote-and-definitions-desktop");
        await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await checkLayout();
        await page.evaluate('document.querySelector("h2").scrollIntoView({behavior: "instant"})');
        await page.screenshot("textpod-quote-and-definitions-mobile");
    });

    await t.test("mindmaps use their natural width and stay centered beyond the text column", async () => {
        await page.send("Emulation.setDeviceMetricsOverride", { width: 2000, height: 1000, deviceScaleFactor: 1, mobile: false });
        await page.navigate(new URL("note/20260824091055", preview).href);
        const readMaps = () => page.evaluate(`(() => {
            const textWidth = document.querySelector('.note-content').getBoundingClientRect().width;
            const maps = [...document.querySelectorAll('pre.mindmap')].map(map => {
                const rect = map.getBoundingClientRect();
                const frame = map.parentElement.getBoundingClientRect();
                return {
                    width: rect.width, frameWidth: frame.width,
                    center: rect.left + rect.width / 2,
                    left: rect.left, right: rect.right,
                    scrolling: map.scrollWidth > map.clientWidth,
                };
            });
            return {textWidth, viewport: innerWidth, maps};
        })()`);
        const desktop = await readMaps();
        assert.ok(desktop.maps.length >= 2);
        assert.ok(desktop.maps[0].width < desktop.maps[0].frameWidth - 8);
        assert.ok(desktop.maps[1].width > desktop.textWidth * 1.3);
        for (const map of desktop.maps) {
            assert.ok(Math.abs(map.center - desktop.viewport / 2) < 2);
            assert.ok(map.left >= 0 && map.right <= desktop.viewport);
        }
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.evaluate('document.querySelectorAll("pre.mindmap")[1].scrollIntoView({behavior: "instant"})');
        await page.screenshot("textpod-mindmap-natural-width");
        await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        const mobile = await readMaps();
        for (const map of mobile.maps) {
            assert.ok(Math.abs(map.center - mobile.viewport / 2) < 2);
            assert.ok(map.left >= 0 && map.right <= mobile.viewport);
        }
        assert.equal(mobile.maps[1].scrolling, true);
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
        await page.screenshot("textpod-mindmap-mobile");
    });

    await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });
    await page.navigate(new URL("note/20260904180157", preview).href);
    await page.screenshot("textpod-heading-descenders");

    await t.test("search has a compact header and a useful empty state", async () => {
        await page.navigate(preview + "?q=textpod-no-match-7cd9f3");
        assert.equal(await page.evaluate('getComputedStyle(document.querySelector(".hero")).display'), "none");
        assert.equal(await page.evaluate('document.querySelector(".empty-state") !== null'), true);
        assert.equal(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
    });
});
