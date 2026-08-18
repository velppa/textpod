;;; textpod.el --- Interact with Textpod from Emacs -*- lexical-binding: t -*-

;; Copyright (C) 2026 Pavel Popov

;; Author: Pavel Popov
;; Version: 0.3.1
;; Package-Requires: ((emacs "28.1") (plz "0.7"))
;; Keywords: convenience, comm

;; This program is free software; you can redistribute it and/or modify
;; it under the terms of the GNU General Public License as published by
;; the Free Software Foundation, either version 3 of the License, or
;; (at your option) any later version.

;;; Commentary:

;; Send notes from Emacs to a Textpod instance.
;;
;; `textpod-org-region-to-note' converts the selected Org region to
;; Markdown and POSTs it to Textpod.

;;; Code:

(require 'plz)
(require 'ox-md)
(require 'ox-html)
(require 'rx)
(require 'org)
(require 'url-util)

;;;; Customization

(defgroup textpod nil
  "Settings for `textpod'."
  :group 'comm)

(defcustom textpod-url "http://localhost:3000"
  "Base URL of the Textpod server."
  :type 'string
  :group 'textpod)

(defcustom textpod-token nil
  "Bearer token for Textpod authentication.
When non-nil, sent as an Authorization header with write requests."
  :type '(choice (const :tag "None" nil) string)
  :group 'textpod)

(defcustom textpod-id-property "TEXTPOD_ID"
  "Name of the Org property used to store the Textpod note id."
  :type 'string
  :group 'textpod)

(defcustom textpod-id-prefix ""
  "Prefix prepended to Textpod note ids when stored in the Org property.
The prefix is stripped before sending requests to the Textpod server."
  :type 'string
  :group 'textpod)

;;;; Commands

;;;###autoload
(defun textpod-set-id (&optional time)
  "Set `textpod-id-property' on the current heading without sending to server.
With prefix arg, prompt for TIME with `org-read-date'; otherwise use now.
The id is derived from TIME as YYYYMMDDhhmmss and prefixed with
`textpod-id-prefix'.  Useful for back-dating notes before uploading."
  (interactive
   (list (when current-prefix-arg
           (org-read-date t t nil "Timestamp: "))))
  (let* ((time (or time (current-time)))
         (id (format-time-string "%Y%m%d%H%M%S" time))
         (value (concat textpod-id-prefix id)))
    (save-excursion
      (org-back-to-heading t)
      (org-set-property textpod-id-property value))
    (message "Set %s to %s" textpod-id-property value)))

;;;###autoload
(defun textpod-open-current-note ()
  "Open the current heading's Textpod note in a browser."
  (interactive)
  (let ((id (textpod--strip-prefix (org-entry-get nil textpod-id-property t))))
    (unless id
      (user-error "No %s property on this heading" textpod-id-property))
    (browse-url (format "%s/note/%s" textpod-url id))))


;;;###autoload
(defun textpod-org-heading-to-note ()
  "Send the current heading's subtree to Textpod.
Finds the nearest ancestor heading that has a `textpod-id-property'
\(or the current heading itself) and sends it via
`textpod-org-region-to-note'.  If no ancestor has the property,
uses the current top-level heading."
  (interactive)
  (save-excursion
    (org-back-to-heading t)
    ;; Walk up to find a heading with textpod-id-property, stop at level 1.
    (while (and (not (org-entry-get nil textpod-id-property))
                (> (org-current-level) 1))
      (org-up-heading-safe))
    (let ((beg (point))
          (end (org-end-of-subtree t t)))
      (textpod-org-region-to-note beg end))))


;;;###autoload
(defun textpod-org-region-to-note (beg end)
  "Convert the Org region between BEG and END to Markdown and send to Textpod."
  (interactive "r")
  (let* ((buf (current-buffer))
         (heading-marker (with-current-buffer buf
                           (save-excursion
                             (goto-char beg)
                             (org-back-to-heading t)
                             (point-marker))))
         (existing-id (textpod--strip-prefix
                       (with-current-buffer buf
                         (save-excursion
                           (goto-char heading-marker)
                           (org-entry-get nil textpod-id-property)))))
         (org-text (buffer-substring-no-properties beg end))
         (org-text (textpod--replace-checkboxes org-text))
         (org-text (textpod--strip-statistics-cookies org-text))
         (org-text (textpod--process-details org-text))
         (out (let ((org-export-with-toc nil)
                    (org-export-with-todo-keywords nil)
                    (org-html-htmlize-output-type nil)
                    (org-md-headline-style 'atx))
                (org-export-string-as org-text 'textpod-html t)))
         (out (textpod--wrap-details out))
         (out (textpod--add-image-dimensions out default-directory))
         (out (textpod--upload-local-links out default-directory))
         (json-body (json-encode out)))
    (if existing-id
        (plz 'put (concat textpod-url "/notes/" existing-id)
          :headers (textpod--headers)
          :body json-body
          :then (lambda (_) (message "Note updated in Textpod: %s" existing-id))
          :else (lambda (err) (message "Textpod error: %s" err)))
      (plz 'post (concat textpod-url "/notes")
        :headers (textpod--headers)
        :body json-body
        :as 'string
        :then (lambda (body)
                (let ((id (json-parse-string body)))
                  (with-current-buffer (marker-buffer heading-marker)
                    (save-excursion
                      (goto-char heading-marker)
                      (org-set-property textpod-id-property
                                        (concat textpod-id-prefix id))))
                  (message "Note sent to Textpod: %s" id)))
        :else (lambda (err) (message "Textpod error: %s" err))))))

;;;; Functions

(defun textpod--strip-prefix (id)
  "Return ID with `textpod-id-prefix' stripped, or nil if ID is nil."
  (when id
    (if (and (not (string-empty-p textpod-id-prefix))
             (string-prefix-p textpod-id-prefix id))
        (substring id (length textpod-id-prefix))
      id)))

(defun textpod--headers ()
  "Return HTTP headers for Textpod requests."
  (let ((hdrs '(("Content-Type" . "application/json"))))
    (when textpod-token
      (push (cons "Authorization" (concat "Bearer " textpod-token)) hdrs))
    hdrs))

(defun textpod--process-details (org-text)
  "Replace #+DETAILS: directives in ORG-TEXT with unique markers.
Returns the modified text.  Markers survive org export as literal text
and are replaced with HTML <details> tags by `textpod--wrap-details'."
  (let ((counter 0))
    (replace-regexp-in-string
     (rx bol "#+DETAILS:" (* space) (group (+ nonl)))
     (lambda (match)
       (let ((summary (match-string 1 match)))
         (setq counter (1+ counter))
         (format "TEXTPODDETAILSOPEN%d{%s}" counter summary)))
     org-text)))

(defun textpod--wrap-details (md)
  "Replace TEXTPOD_DETAILS markers in MD with HTML <details> tags.
Each marker opens a new <details> block; the previous one is closed."
  (let ((re (rx "TEXTPODDETAILSOPEN" (+ digit) "{" (group (+? anything)) "}"))
        (has-open nil))
    (if (not (string-match-p re md))
        md
      (let ((result (replace-regexp-in-string
                     re
                     (lambda (match)
                       (let ((summary (match-string 1 match))
                             (prefix (if has-open "</details>\n" "")))
                         (setq has-open t)
                         (format "%s<details><summary>%s</summary>\n" prefix summary)))
                     md)))
        (if has-open
            (concat result "\n</details>")
          result)))))

(defun textpod--replace-checkboxes (text)
  "Replace Org checkbox markers in TEXT with emoji equivalents."
  (let ((re (rx bol (* space) "-" (+ space)
                (group "[" (any " " "X" "-") "]"))))
    (replace-regexp-in-string
     re
     (lambda (match)
       (let ((marker (match-string 1 match)))
         (replace-regexp-in-string
          (regexp-quote marker)
          (pcase marker
            ("[ ]" "☐")
            ("[X]" "☑")
            ("[-]" "▣"))
          match t t)))
     text)))

(defun textpod--strip-statistics-cookies (text)
  "Remove Org statistics cookies like [3/3] or [100%] from TEXT."
  (replace-regexp-in-string
   (rx " " "[" (or (seq (+ digit) "/" (+ digit))
                    (seq (+ digit) "%"))
       "]")
   "" text))

(defun textpod--add-image-dimensions (html base-dir)
  "Add width/height attributes to <img> tags in HTML for retina displays.
Reads actual pixel dimensions, halves them (assuming 2x retina),
and sets explicit attributes so images display at logical size.
BASE-DIR is used to resolve relative src paths."
  (replace-regexp-in-string
   (rx "<img src=\"" (group (+ (not "\""))) "\"" (group (* (not ">"))) ">")
   (lambda (match)
     (let* ((src (match-string 1 match))
            (rest (match-string 2 match))
            (abs-path (if (or (string-prefix-p "http:" src)
                              (string-prefix-p "https:" src))
                          nil
                        (if (file-name-absolute-p src)
                            src
                          (expand-file-name src base-dir)))))
       (if (and abs-path (file-exists-p abs-path))
           (let ((size (image-size (create-image abs-path nil nil :scale 1) t)))
             (if size
                 (let ((w (/ (car size) 2))
                       (h (/ (cdr size) 2)))
                   (format "<img src=\"%s\" width=\"%d\" height=\"%d\"%s>"
                           src w h rest))
               match))
         match)))
   html))

(defun textpod--auth-headers ()
  "Return auth headers (no Content-Type)."
  (when textpod-token
    `(("Authorization" . ,(concat "Bearer " textpod-token)))))

(defun textpod--asset-exists-p (name)
  "Return non-nil if asset NAME already exists on the server."
  (condition-case nil
      (let ((resp (plz 'head (concat textpod-url "/assets/" name)
                    :headers (textpod--auth-headers)
                    :as 'response)))
        (= 204 (plz-response-status resp)))
    (error nil)))

(defun textpod--upload-asset (file-path)
  "Upload FILE-PATH to Textpod assets.  Return the remote URL.
Skips upload if the asset already exists."
  (let ((name (file-name-nondirectory file-path)))
    (unless (textpod--asset-exists-p name)
      (plz 'put (concat textpod-url "/assets/" name)
        :headers (append (textpod--auth-headers)
                         '(("Content-Type" . "application/octet-stream")))
        :body-type 'binary
        :body `(file ,file-path)))
    (concat textpod-url "/assets/" name)))

(defun textpod--local-path (path base-dir)
  "Return the absolute local file PATH resolved against BASE-DIR.
Decodes file:// URIs.  Return nil for empty paths and non-file
schemes (http:, https:, mailto:, ...)."
  (cond
   ((string-prefix-p "file://" path)
    (url-unhex-string (substring path (length "file://"))))
   ((string-match-p (rx bos (+ (any "a-zA-Z")) ":") path) nil)
   ((string-empty-p path) nil)
   ((file-name-absolute-p path) path)
   (t (expand-file-name path base-dir))))

(defun textpod--upload-local-links (html base-dir)
  "Find local file/image links in HTML, upload them, rewrite to remote URLs.
BASE-DIR is the directory to resolve relative paths against.
Matches both HTML attributes (src=\"...\", href=\"...\") and markdown
link syntax (![...](...), [...](...)). Returns the modified string."
  (let ((md-re (rx (or "![" "[")
                   (group (*? anything))
                   "]("
                   (group (*? anything))
                   ")"))
        (html-re (rx (or "src" "href") "=\""
                     (group (*? anything))
                     "\"")))
    ;; First pass: HTML src/href attributes
    (setq html
          (replace-regexp-in-string
           html-re
           (lambda (match)
             (let* ((path (match-string 1 match))
                    (attr (substring match 0 (string-match-p "=\"" match)))
                    (abs-path (save-match-data
                                (textpod--local-path path base-dir))))
               (if (and abs-path (file-exists-p abs-path))
                   (let ((url (save-match-data (textpod--upload-asset abs-path))))
                     (format "%s=\"%s\"" attr url))
                 match)))
           html))
    ;; Second pass: markdown-style links (if any survive)
    (replace-regexp-in-string
     md-re
     (lambda (match)
       (let* ((label (match-string 1 match))
              (path (match-string 2 match))
              (is-image (string-prefix-p "!" (substring match 0 1)))
              (abs-path (save-match-data
                          (textpod--local-path path base-dir))))
         (if (and abs-path (file-exists-p abs-path))
             (let ((url (save-match-data (textpod--upload-asset abs-path))))
               (if is-image
                   (format "![%s](%s)" label url)
                 (format "[%s](%s)" label url)))
           match)))
     html)))

;;;; Org export backend

;; An Org HTML backend that produces body-only HTML fragments
;; (sidenotes, margin notes, epigraph quotes, captioned src blocks)
;; suitable for embedding into Textpod notes.  It never emits a full
;; document template, never wraps sections in <section>, and adds no
;; attribution footer.

(defconst textpod--no-footnotes-section ""
  "Footnotes-section template that emits no output.
Footnotes are rendered inline as sidenotes, so the trailing
footnotes block produced by `ox-html' is suppressed entirely.
An empty string is used (rather than an HTML comment) because the
exported HTML is then re-rendered by goldmark, which can split
comments at blank lines and leak the closing `-->' as text.")

(org-export-define-derived-backend 'textpod-html 'html
  :options-alist
  `((:html-footnotes-section nil nil ,textpod--no-footnotes-section)
    ;; Top-level Org headline (`* Foo') maps to <h1> instead of the
    ;; ox-html default <h2>.  Notes are body fragments embedded into
    ;; a host page; there is no document title to reserve <h1> for.
    (:html-toplevel-hlevel nil "H" 1))
  :translate-alist
  '((footnote-reference . textpod--footnote-reference)
    (headline           . textpod--headline)
    (link               . textpod--link)
    (quote-block        . textpod--quote-block)
    (src-block          . textpod--src-block)))

(defun textpod--tags-span-to-colons (html)
  "Rewrite ox-html's `<span class=\"tag\">…</span>' block in HTML to
`:tag1:tag2:' colon form.

ox-html renders Org headline tags as nested spans:
  <span class=\"tag\"><span class=\"t1\">t1</span>&#xa0;<span …>t2</span></span>
The Textpod server's tag handler scans heading inner text for the
`:Tag:' colon syntax, so we convert the span back to that form
(prefixed with a single space) and let the server style it.
Returns HTML unchanged if it isn't a string."
  (if (not (stringp html))
      html
    (replace-regexp-in-string
     "\\(?:&#xa0;\\|[ \t\n]\\)*<span class=\"tag\">\\(?:<span class=\"[^\"]+\">[^<]+</span>\\(?:&#xa0;\\)?\\)+</span>"
     (lambda (match)
       ;; The inner-tag regex requires `[^<]+' between the open/close
       ;; spans, so the outer `<span class="tag">' wrapper (which
       ;; contains nested `<') is skipped automatically — only the
       ;; per-tag inner spans are captured.
       ;; `save-match-data' is required: the inner `string-match'
       ;; would otherwise clobber the outer replace's match data,
       ;; causing only a tail slice of the span to be substituted.
       (save-match-data
         (let ((tags '())
               (start 0))
           (while (string-match "<span class=\"[^\"]+\">\\([^<]+\\)</span>"
                                match start)
             (push (match-string 1 match) tags)
             (setq start (match-end 0)))
           (if tags
               (concat " :" (mapconcat #'identity (nreverse tags) ":") ":")
             ""))))
     html t t)))

(defun textpod--headline (headline contents info)
  "Render headlines with <article>/<section> wrappers instead of
the default <div class=\"outline-N\"> / <div class=\"outline-text-N\">.

The page's CSS width rules target `section > p' / `section > table'
etc.; only direct children of <section> get the narrow-column layout.
The default ox-html wrappers are <div>s, so those rules never
fire.  We swap:

  <div ... class=\"outline-1\">          → <article>   (top-level only)
  <div ... class=\"outline-N>1\">        → <section>
  <div ... class=\"outline-text-N\">     → <section>

leaving headline tags and child subtree blocks untouched.  Only
the outermost top-level headline becomes an <article>; nested
subheadings become <section>s so each h2 isn't wrapped in its own
<article>.

Falls back to `org-html-headline' for list-style headlines, where
the output isn't a wrapper-div pair and rewriting would corrupt
structure."
  (let ((html (textpod--tags-span-to-colons
               (org-html-headline headline contents info)))
        (level (org-export-get-relative-level headline info)))
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
                (if (= level 1)
                    (concat "<article>" inner "</article>\n")
                  (concat "<section>" inner "</section>\n")))
            html))
      html)))

(defun textpod--footnote-reference (footnote-reference _contents info)
  "Render FOOTNOTE-REFERENCE as a sidenote.
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

(defun textpod--link (link desc info)
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

(defun textpod--quote-block (quote-block contents _info)
  "Render QUOTE-BLOCK as an epigraph.
A `#+NAME:' on the block becomes the footer attribution."
  (let ((name (org-element-property :name quote-block)))
    (format "<div class=\"epigraph\"><blockquote>\n%s%s</blockquote></div>"
            contents
            (if name (format "<footer>%s</footer>" name) ""))))

(defun textpod--src-block (src-block _contents info)
  "Render SRC-BLOCK; wrap in <details> when it has a caption."
  (let ((caption (org-export-get-caption src-block))
        (code (org-html-format-code src-block info)))
    (if caption
        (format "<details><summary>%s</summary><pre class=\"code\"><code>%s</code></pre></details>"
                (org-trim (org-export-data caption info))
                code)
      (format "<pre class=\"code\"><code>%s</code></pre>" code))))

;;;###autoload
(defun textpod-export-as-string (org-text)
  "Export ORG-TEXT to an HTML fragment string."
  (let ((org-export-with-toc nil)
        (org-export-with-todo-keywords nil)
        (org-export-with-section-numbers nil))
    (org-export-string-as org-text 'textpod-html t)))

;;;###autoload
(defun textpod-export-to-buffer ()
  "Export current buffer or region to an HTML buffer (body-only)."
  (interactive)
  (org-export-to-buffer 'textpod-html "*Textpod Export*"
    nil nil nil t nil (lambda () (html-mode))))

;;;; Footer

(provide 'textpod)

;;; textpod.el ends here
