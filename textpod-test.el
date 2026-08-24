;;; textpod-test.el --- Tests for textpod -*- lexical-binding: t; -*-

;;; Commentary:

;; ERT tests for `textpod'.  Run from the project root with:
;;
;;   emacs --batch -L . -l textpod-test.el \
;;     -f ert-run-tests-batch-and-exit

;;; Code:

(require 'ert)
(require 'ox)
(require 'ox-html)

;; `textpod' depends on `plz', installed via package.el.
(require 'package)
(package-initialize)

(load (expand-file-name "textpod.el"
                        (file-name-directory
                         (or load-file-name buffer-file-name))))

(defun textpod-test--export (org-text)
  "Export ORG-TEXT through the textpod backend, returning the string."
  (let ((org-export-with-toc nil)
        (org-export-with-section-numbers nil)
        (org-export-with-todo-keywords nil))
    (org-export-string-as org-text 'textpod-html t)))


;;;; tags-span-to-colons

(ert-deftest textpod-test/tags-single ()
  "A single ox-html tag span collapses to `:tag:'."
  (should
   (equal
    (textpod--tags-span-to-colons
     (concat "<h2>Title&#xa0;&#xa0;&#xa0;"
             "<span class=\"tag\">"
             "<span class=\"Blog\">Blog</span>"
             "</span></h2>"))
    "<h2>Title :Blog:</h2>")))

(ert-deftest textpod-test/tags-multiple ()
  "Multiple tags inside one span collapse to `:t1:t2:t3:' (regression).
Regression: a missing `save-match-data' around the inner
`string-match' caused only the trailing inner span to be
replaced, leaving the leading spans untouched."
  (should
   (equal
    (textpod--tags-span-to-colons
     (concat "<h2>Title&#xa0;&#xa0;&#xa0;"
             "<span class=\"tag\">"
             "<span class=\"Blog\">Blog</span>&#xa0;"
             "<span class=\"Tech\">Tech</span>&#xa0;"
             "<span class=\"Coding\">Coding</span>"
             "</span></h2>"))
    "<h2>Title :Blog:Tech:Coding:</h2>")))

(ert-deftest textpod-test/tags-no-tag-span ()
  "Headings without a tag span are returned unchanged."
  (let ((html "<h2>Plain heading</h2>"))
    (should (equal (textpod--tags-span-to-colons html) html))))

(ert-deftest textpod-test/tags-non-string ()
  "Non-string input passes through untouched."
  (should (eq (textpod--tags-span-to-colons nil) nil))
  (should (eq (textpod--tags-span-to-colons 42) 42)))

(ert-deftest textpod-test/tags-multiple-headings ()
  "Replacement is performed for every tag span in the input."
  (should
   (equal
    (textpod--tags-span-to-colons
     (concat "<h2>One <span class=\"tag\">"
             "<span class=\"A\">A</span>&#xa0;"
             "<span class=\"B\">B</span></span></h2>"
             "<h2>Two <span class=\"tag\">"
             "<span class=\"C\">C</span></span></h2>"))
    "<h2>One :A:B:</h2><h2>Two :C:</h2>")))


;;;; End-to-end export

(ert-deftest textpod-test/export-headline-tags ()
  "An Org headline with multiple tags exports with colon-form tags."
  (let ((html (textpod-test--export
               "* Note title :Blog:Tech:Coding:\nBody.\n")))
    (should (string-match-p ":Blog:Tech:Coding:" html))
    (should-not (string-match-p "<span class=\"tag\">" html))))

(ert-deftest textpod-test/export-headline-wraps-article ()
  "Top-level headline becomes <article>; nested becomes <section>."
  (let ((html (textpod-test--export
               "* Top\n** Inner\nBody.\n")))
    (should (string-match-p "<article>" html))
    (should (string-match-p "</article>" html))
    (should (string-match-p "<section>" html))))

(ert-deftest textpod-test/export-footnote-becomes-sidenote ()
  "Footnote references render as sidenote markup."
  (let ((html (textpod-test--export
               "Body[fn:1] text.\n\n[fn:1] Footnote text.\n")))
    (should (string-match-p "class=\"margin-toggle sidenote-number\"" html))
    (should (string-match-p "class=\"sidenote\">[^<]*Footnote text" html))
    ;; The default ox-html footnotes section is suppressed.
    (should-not (string-match-p "id=\"footnotes\"" html))))

(ert-deftest textpod-test/export-margin-note-link ()
  "An `mn:LABEL' fuzzy link renders as marginnote markup."
  (let ((html (textpod-test--export
               "Body [[mn:foo][note text]] more.\n")))
    (should (string-match-p "class=\"margin-toggle\"" html))
    (should (string-match-p "class=\"marginnote\">note text" html))))

(ert-deftest textpod-test/export-quote-block-epigraph ()
  "A named quote block renders as an epigraph with a footer."
  (let ((html (textpod-test--export
               "#+NAME: Author\n#+BEGIN_QUOTE\nA saying.\n#+END_QUOTE\n")))
    (should (string-match-p "<div class=\"epigraph\">" html))
    (should (string-match-p "<blockquote>" html))
    (should (string-match-p "<footer>Author</footer>" html))))

(ert-deftest textpod-test/export-quote-block-no-name ()
  "A quote block without `#+NAME' has no footer attribution."
  (let ((html (textpod-test--export
               "#+BEGIN_QUOTE\nA saying.\n#+END_QUOTE\n")))
    (should (string-match-p "<div class=\"epigraph\">" html))
    (should-not (string-match-p "<footer>" html))))

(ert-deftest textpod-test/export-src-block-no-caption ()
  "Src blocks without captions render as bare <pre class=\"code\">."
  (let ((html (textpod-test--export
               "#+BEGIN_SRC emacs-lisp\n(+ 1 2)\n#+END_SRC\n")))
    (should (string-match-p "<pre class=\"code\">" html))
    (should-not (string-match-p "<details>" html))))

(ert-deftest textpod-test/export-src-block-caption-wraps-details ()
  "A captioned src block is wrapped in <details>/<summary>."
  (let ((html (textpod-test--export
               "#+CAPTION: My snippet\n#+BEGIN_SRC emacs-lisp\n(+ 1 2)\n#+END_SRC\n")))
    (should (string-match-p "<details><summary>My snippet</summary>" html))
    (should (string-match-p "<pre class=\"code\">" html))))


;;;; special-block / mindmap

(ert-deftest textpod-test/export-mindmap-block-is-pre ()
  "A mindmap block renders as a verbatim <pre class=\"mindmap\">,
preserving indentation and line breaks (regression: the default
special-block export wraps the body in <p>, whose HTML carries no
`white-space: pre', so a browser collapses the ASCII-art layout)."
  (let ((html (textpod-test--export
               "#+begin_mindmap\n  a ─┬─ b\n     ╰─ c\n#+end_mindmap\n")))
    (should (string-match-p "<pre class=\"mindmap\">" html))
    (should (string-match-p "  a ─┬─ b\n     ╰─ c" html))
    (should-not (string-match-p "<p>" html))))

(ert-deftest textpod-test/export-mindmap-block-not-reinterpreted ()
  "Mindmap content is pulled from the raw buffer, not from the
parsed-and-re-exported paragraph, so characters that look like Org
emphasis or HTML markup pass through literally."
  (let ((html (textpod-test--export
               "#+begin_mindmap\n*not bold* <tag> a_b\n#+end_mindmap\n")))
    (should (string-match-p (regexp-quote "*not bold* &lt;tag&gt; a_b") html))
    (should-not (string-match-p "<b>\\|<em>" html))))

(ert-deftest textpod-test/export-other-special-block-unchanged ()
  "A special block that isn't \"mindmap\" keeps the default rendering."
  (let ((html (textpod-test--export
               "#+begin_note\nJust a note.\n#+end_note\n")))
    (should (string-match-p "<div class=\"note\"" html))
    (should-not (string-match-p "<pre class=\"mindmap\">" html))))


;;;; local-path

(ert-deftest textpod-test/local-path-file-uri ()
  "file:// URIs decode to plain absolute paths (regression:
they were treated as relative paths and never uploaded)."
  (should (equal (textpod--local-path "file:///Users/me/pic.jpeg" "/base")
                 "/Users/me/pic.jpeg"))
  (should (equal (textpod--local-path "file:///a/with%20space.png" "/base")
                 "/a/with space.png")))

(ert-deftest textpod-test/local-path-schemes-skipped ()
  "Non-file schemes and empty paths return nil."
  (should-not (textpod--local-path "https://example.com/x.png" "/base"))
  (should-not (textpod--local-path "http://example.com/x.png" "/base"))
  (should-not (textpod--local-path "mailto:me@example.com" "/base"))
  (should-not (textpod--local-path "" "/base")))

(ert-deftest textpod-test/local-path-plain ()
  "Plain paths resolve against BASE-DIR; absolute ones pass through."
  (should (equal (textpod--local-path "/abs/pic.png" "/base") "/abs/pic.png"))
  (should (equal (textpod--local-path "rel/pic.png" "/base") "/base/rel/pic.png")))

(ert-deftest textpod-test/upload-local-links-file-uri ()
  "HTML with file:// src/href gets uploaded and rewritten."
  (let* ((tmp (make-temp-file "textpod-asset-" nil ".el" ";; hi\n"))
         (name (file-name-nondirectory tmp))
         (uploaded nil))
    (unwind-protect
        (cl-letf (((symbol-function 'textpod--upload-asset)
                   (lambda (path)
                     (push path uploaded)
                     (concat "https://host/notes/assets/"
                             (file-name-nondirectory path)))))
          (let ((html (textpod--upload-local-links
                       (format "<a href=\"file://%s\">code</a>" tmp)
                       "/base")))
            (should (equal uploaded (list tmp)))
            (should (equal html (format "<a href=\"https://host/notes/assets/%s\">code</a>"
                                        name)))))
      (delete-file tmp))))


;;;; upload-asset hash comparison

(ert-deftest textpod-test/file-sha256 ()
  "Digest matches a known sha256."
  (let ((tmp (make-temp-file "textpod-hash-" nil nil "hello asset\n")))
    (unwind-protect
        (should (equal (textpod--file-sha256 tmp)
                       (secure-hash 'sha256 "hello asset\n")))
      (delete-file tmp))))

(defun textpod-test--upload-with-etag (etag)
  "Run `textpod--upload-asset' against a server reporting ETAG.
Return non-nil when the asset body was uploaded."
  (let ((tmp (make-temp-file "textpod-hash-" nil nil "hello asset\n"))
        (uploaded nil))
    (unwind-protect
        (cl-letf (((symbol-function 'textpod--asset-etag)
                   (lambda (_name) etag))
                  ((symbol-function 'plz)
                   (lambda (&rest _) (setq uploaded t))))
          (textpod--upload-asset tmp)
          uploaded)
      (delete-file tmp))))

(ert-deftest textpod-test/upload-asset-same-hash-skips ()
  (should-not (textpod-test--upload-with-etag
               (secure-hash 'sha256 "hello asset\n"))))

(ert-deftest textpod-test/upload-asset-changed-hash-reuploads ()
  (should (textpod-test--upload-with-etag
           (secure-hash 'sha256 "old content\n"))))

(ert-deftest textpod-test/upload-asset-absent-uploads ()
  "Missing asset (or pre-ETag server) uploads."
  (should (textpod-test--upload-with-etag nil)))

(provide 'textpod-test)
;;; textpod-test.el ends here
