;;; textpod-org-tufte-test.el --- Tests for textpod-org-tufte -*- lexical-binding: t; -*-

;;; Commentary:

;; ERT tests for `textpod-org-tufte'.  Run from the project root with:
;;
;;   emacs --batch -L . -l textpod-org-tufte-test.el \
;;     -f ert-run-tests-batch-and-exit

;;; Code:

(require 'ert)
(require 'ox)
(require 'ox-html)

(load (expand-file-name "textpod-org-tufte.el"
                        (file-name-directory
                         (or load-file-name buffer-file-name))))

(defun textpod-org-tufte-test--export (org-text)
  "Export ORG-TEXT through the textpod backend, returning the string."
  (let ((org-export-with-toc nil)
        (org-export-with-section-numbers nil)
        (org-export-with-todo-keywords nil))
    (org-export-string-as org-text 'textpod-tufte-html t)))


;;;; tags-span-to-colons

(ert-deftest textpod-org-tufte-test/tags-single ()
  "A single ox-html tag span collapses to `:tag:'."
  (should
   (equal
    (textpod-org-tufte--tags-span-to-colons
     (concat "<h2>Title&#xa0;&#xa0;&#xa0;"
             "<span class=\"tag\">"
             "<span class=\"Blog\">Blog</span>"
             "</span></h2>"))
    "<h2>Title :Blog:</h2>")))

(ert-deftest textpod-org-tufte-test/tags-multiple ()
  "Multiple tags inside one span collapse to `:t1:t2:t3:' (regression).
Regression: a missing `save-match-data' around the inner
`string-match' caused only the trailing inner span to be
replaced, leaving the leading spans untouched."
  (should
   (equal
    (textpod-org-tufte--tags-span-to-colons
     (concat "<h2>Title&#xa0;&#xa0;&#xa0;"
             "<span class=\"tag\">"
             "<span class=\"Blog\">Blog</span>&#xa0;"
             "<span class=\"Tech\">Tech</span>&#xa0;"
             "<span class=\"Coding\">Coding</span>"
             "</span></h2>"))
    "<h2>Title :Blog:Tech:Coding:</h2>")))

(ert-deftest textpod-org-tufte-test/tags-no-tag-span ()
  "Headings without a tag span are returned unchanged."
  (let ((html "<h2>Plain heading</h2>"))
    (should (equal (textpod-org-tufte--tags-span-to-colons html) html))))

(ert-deftest textpod-org-tufte-test/tags-non-string ()
  "Non-string input passes through untouched."
  (should (eq (textpod-org-tufte--tags-span-to-colons nil) nil))
  (should (eq (textpod-org-tufte--tags-span-to-colons 42) 42)))

(ert-deftest textpod-org-tufte-test/tags-multiple-headings ()
  "Replacement is performed for every tag span in the input."
  (should
   (equal
    (textpod-org-tufte--tags-span-to-colons
     (concat "<h2>One <span class=\"tag\">"
             "<span class=\"A\">A</span>&#xa0;"
             "<span class=\"B\">B</span></span></h2>"
             "<h2>Two <span class=\"tag\">"
             "<span class=\"C\">C</span></span></h2>"))
    "<h2>One :A:B:</h2><h2>Two :C:</h2>")))


;;;; End-to-end export

(ert-deftest textpod-org-tufte-test/export-headline-tags ()
  "An Org headline with multiple tags exports with colon-form tags."
  (let ((html (textpod-org-tufte-test--export
               "* Note title :Blog:Tech:Coding:\nBody.\n")))
    (should (string-match-p ":Blog:Tech:Coding:" html))
    (should-not (string-match-p "<span class=\"tag\">" html))))

(ert-deftest textpod-org-tufte-test/export-headline-wraps-article ()
  "Top-level headline becomes <article>; nested becomes <section>."
  (let ((html (textpod-org-tufte-test--export
               "* Top\n** Inner\nBody.\n")))
    (should (string-match-p "<article>" html))
    (should (string-match-p "</article>" html))
    (should (string-match-p "<section>" html))))

(ert-deftest textpod-org-tufte-test/export-footnote-becomes-sidenote ()
  "Footnote references render as Tufte sidenote markup."
  (let ((html (textpod-org-tufte-test--export
               "Body[fn:1] text.\n\n[fn:1] Footnote text.\n")))
    (should (string-match-p "class=\"margin-toggle sidenote-number\"" html))
    (should (string-match-p "class=\"sidenote\">[^<]*Footnote text" html))
    ;; The default ox-html footnotes section is suppressed.
    (should-not (string-match-p "id=\"footnotes\"" html))))

(ert-deftest textpod-org-tufte-test/export-margin-note-link ()
  "An `mn:LABEL' fuzzy link renders as marginnote markup."
  (let ((html (textpod-org-tufte-test--export
               "Body [[mn:foo][note text]] more.\n")))
    (should (string-match-p "class=\"margin-toggle\"" html))
    (should (string-match-p "class=\"marginnote\">note text" html))))

(ert-deftest textpod-org-tufte-test/export-quote-block-epigraph ()
  "A named quote block renders as an epigraph with a footer."
  (let ((html (textpod-org-tufte-test--export
               "#+NAME: Author\n#+BEGIN_QUOTE\nA saying.\n#+END_QUOTE\n")))
    (should (string-match-p "<div class=\"epigraph\">" html))
    (should (string-match-p "<blockquote>" html))
    (should (string-match-p "<footer>Author</footer>" html))))

(ert-deftest textpod-org-tufte-test/export-quote-block-no-name ()
  "A quote block without `#+NAME' has no footer attribution."
  (let ((html (textpod-org-tufte-test--export
               "#+BEGIN_QUOTE\nA saying.\n#+END_QUOTE\n")))
    (should (string-match-p "<div class=\"epigraph\">" html))
    (should-not (string-match-p "<footer>" html))))

(ert-deftest textpod-org-tufte-test/export-src-block-no-caption ()
  "Src blocks without captions render as bare <pre class=\"code\">."
  (let ((html (textpod-org-tufte-test--export
               "#+BEGIN_SRC emacs-lisp\n(+ 1 2)\n#+END_SRC\n")))
    (should (string-match-p "<pre class=\"code\">" html))
    (should-not (string-match-p "<details>" html))))

(ert-deftest textpod-org-tufte-test/export-src-block-caption-wraps-details ()
  "A captioned src block is wrapped in <details>/<summary>."
  (let ((html (textpod-org-tufte-test--export
               "#+CAPTION: My snippet\n#+BEGIN_SRC emacs-lisp\n(+ 1 2)\n#+END_SRC\n")))
    (should (string-match-p "<details><summary>My snippet</summary>" html))
    (should (string-match-p "<pre class=\"code\">" html))))

(provide 'textpod-org-tufte-test)
;;; textpod-org-tufte-test.el ends here
