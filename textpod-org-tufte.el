;;; textpod-org-tufte.el --- Tufte-flavored Org HTML backend for Textpod -*- lexical-binding: t; -*-

;; Author: Pavel Popov
;; Version: 0.1.0
;; Package-Requires: ((emacs "28.1") (org "9.5"))
;; Keywords: org html tufte textpod

;;; Commentary:

;; Provides the `textpod-tufte-html' Org export backend.  It produces
;; Tufte-style HTML fragments (sidenotes, margin notes, epigraph
;; quotes, captioned src blocks) suitable for embedding into Textpod
;; notes.  Unlike `tufte-html' it never emits a full document
;; template, never wraps sections in <section>, and adds no
;; attribution footer — it is meant to be exported body-only and
;; spliced into a host page that already provides scaffolding and
;; CSS.

;;; Code:

(require 'ox)
(require 'ox-html)

(defgroup textpod-org-tufte nil
  "Tufte-flavored HTML export for Textpod."
  :group 'org-export)

(defconst textpod-org-tufte--no-footnotes-section ""
  "Footnotes-section template that emits no output.
Footnotes are rendered inline as Tufte sidenotes, so the trailing
footnotes block produced by `ox-html' is suppressed entirely.
An empty string is used (rather than an HTML comment) because the
exported HTML is then re-rendered by goldmark, which can split
comments at blank lines and leak the closing `-->' as text.")

(org-export-define-derived-backend 'textpod-tufte-html 'html
  :options-alist
  `((:html-footnotes-section nil nil ,textpod-org-tufte--no-footnotes-section)
    ;; Top-level Org headline (`* Foo') maps to <h1> instead of the
    ;; ox-html default <h2>.  Notes are body fragments embedded into
    ;; a host page; there is no document title to reserve <h1> for.
    (:html-toplevel-hlevel nil "H" 1))
  :translate-alist
  '((footnote-reference . textpod-org-tufte-footnote-reference)
    (headline           . textpod-org-tufte-headline)
    (link               . textpod-org-tufte-link)
    (quote-block        . textpod-org-tufte-quote-block)
    (src-block          . textpod-org-tufte-src-block)))

(defun textpod-org-tufte-headline (headline contents info)
  "Render headlines with <article>/<section> wrappers instead of
the default <div class=\"outline-N\"> / <div class=\"outline-text-N\">.

tufte.css's width rules target `section > p' / `section > table'
etc.; only direct children of <section> get the 55%-column layout.
The default ox-html wrappers are <div>s, so those rules never
fire.  We swap:

  <div ... class=\"outline-N\">          → <article>
  <div ... class=\"outline-text-N\">     → <section>

leaving headline tags and child subtree blocks untouched.

Falls back to `org-html-headline' for list-style headlines, where
the output isn't a wrapper-div pair and rewriting would corrupt
structure."
  (let ((html (org-html-headline headline contents info)))
    (if (and (stringp html)
             (string-match
              "\\`<div id=\"outline-container-[^\"]+\" class=\"outline-[0-9]+\"[^>]*>"
              html))
        (let* ((body-start (match-end 0))
               (trimmed (string-trim-right html))
               (close "</div>"))
          (if (string-suffix-p close trimmed)
              (let ((inner (substring trimmed body-start
                                      (- (length trimmed) (length close)))))
                (setq inner
                      (replace-regexp-in-string
                       "<div class=\"outline-text-[0-9]+\" id=\"text-[^\"]+\">"
                       "<section>" inner))
                (setq inner
                      (replace-regexp-in-string
                       "</div>" "</section>" inner))
                (concat "<article>" inner "</article>\n"))
            html))
      html)))

(defun textpod-org-tufte-footnote-reference (footnote-reference _contents info)
  "Render FOOTNOTE-REFERENCE as a Tufte sidenote.
Footnote definitions become inline sidenotes; the host page is
responsible for hiding/showing them via the .margin-toggle
checkbox pattern."
  (let* ((n (org-export-get-footnote-number footnote-reference info))
         (id (format "sn-%s" n))
         (def (org-trim
               (org-export-data
                (org-export-get-footnote-definition footnote-reference info)
                info)))
         (def (replace-regexp-in-string "</?p[^>]*>" "" def)))
    (format
     (concat "<label for=\"%s\" class=\"margin-toggle sidenote-number\"></label>"
             "<input type=\"checkbox\" id=\"%s\" class=\"margin-toggle\"/>"
             "<span class=\"sidenote\">%s</span>")
     id id def)))

(defun textpod-org-tufte-link (link desc info)
  "Render LINK; convert fuzzy `mn:LABEL' links to margin notes.
Any other link falls through to the standard HTML transcoder."
  (let ((path (split-string (or (org-element-property :path link) "") ":")))
    (if (and (string= (org-element-property :type link) "fuzzy")
             (string= (car path) "mn"))
        (let ((id (format "mn-%s" (or (cadr path) (random 1000000))))
              (text (replace-regexp-in-string "</?p[^>]*>" "" (or desc ""))))
          (format
           (concat "<label for=\"%s\" class=\"margin-toggle\">&#8853;</label>"
                   "<input type=\"checkbox\" id=\"%s\" class=\"margin-toggle\"/>"
                   "<span class=\"marginnote\">%s</span>")
           id id text))
      (org-html-link link desc info))))

(defun textpod-org-tufte-quote-block (quote-block contents _info)
  "Render QUOTE-BLOCK as an epigraph.
A `#+NAME:' on the block becomes the footer attribution."
  (let ((name (org-element-property :name quote-block)))
    (format "<div class=\"epigraph\"><blockquote>\n%s%s</blockquote></div>"
            contents
            (if name (format "<footer>%s</footer>" name) ""))))

(defun textpod-org-tufte-src-block (src-block _contents info)
  "Render SRC-BLOCK; wrap in <details> when it has a caption."
  (let ((caption (org-export-get-caption src-block))
        (code (org-html-format-code src-block info)))
    (if caption
        (format "<details><summary>%s</summary><pre class=\"code\"><code>%s</code></pre></details>"
                (org-trim (org-export-data caption info))
                code)
      (format "<pre class=\"code\"><code>%s</code></pre>" code))))

;;;###autoload
(defun textpod-org-tufte-export-as-string (org-text)
  "Export ORG-TEXT to a Tufte-flavored HTML fragment string."
  (let ((org-export-with-toc nil)
        (org-export-with-todo-keywords nil)
        (org-export-with-section-numbers nil))
    (org-export-string-as org-text 'textpod-tufte-html t)))

;;;###autoload
(defun textpod-org-tufte-export-to-buffer ()
  "Export current buffer or region to a Tufte HTML buffer (body-only)."
  (interactive)
  (org-export-to-buffer 'textpod-tufte-html "*Textpod Tufte Export*"
    nil nil nil t nil (lambda () (html-mode))))

(provide 'textpod-org-tufte)
;;; textpod-org-tufte.el ends here
