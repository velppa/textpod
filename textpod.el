;;; textpod.el --- Interact with Textpod from Emacs -*- lexical-binding: t -*-

;; Copyright (C) 2026 Pavel Popov

;; Author: Pavel Popov
;; Version: 0.1.0
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
(require 'rx)
(require 'org)

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

;;;; Commands

;;;###autoload
(defun textpod-open-current-note ()
  "Open the current heading's Textpod note in a browser."
  (interactive)
  (let ((id (org-entry-get nil "TEXTPOD_ID" t)))
    (unless id
      (user-error "No TEXTPOD_ID property on this heading"))
    (browse-url (format "%s/note/%s" textpod-url id))))


;;;###autoload
(defun textpod-org-heading-to-note ()
  "Send the current heading's subtree to Textpod.
Finds the nearest ancestor heading that has a TEXTPOD_ID property
\(or the current heading itself) and sends it via
`textpod-org-region-to-note'.  If no ancestor has TEXTPOD_ID,
uses the current top-level heading."
  (interactive)
  (save-excursion
    (org-back-to-heading t)
    ;; Walk up to find a heading with TEXTPOD_ID, stop at level 1.
    (while (and (not (org-entry-get nil "TEXTPOD_ID"))
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
         (existing-id (with-current-buffer buf
                        (save-excursion
                          (goto-char heading-marker)
                          (org-entry-get nil "TEXTPOD_ID"))))
         (org-text (buffer-substring-no-properties beg end))
         (org-text (textpod--replace-checkboxes org-text))
         (org-text (textpod--strip-statistics-cookies org-text))
         (org-text (textpod--process-details org-text))
         (md (let ((org-export-with-toc nil)
                   (org-export-with-todo-keywords nil)
                   (org-md-headline-style 'atx))
               (org-export-string-as org-text 'md t)))
         (md (textpod--wrap-details md))
         (md (textpod--upload-local-links md default-directory))
         (json-body (json-encode md)))
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
                      (org-set-property "TEXTPOD_ID" id)))
                  (message "Note sent to Textpod: %s" id)))
        :else (lambda (err) (message "Textpod error: %s" err))))))

;;;; Functions

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

(defun textpod--upload-local-links (md base-dir)
  "Find local file links in MD, upload them, rewrite to remote URLs.
BASE-DIR is the directory to resolve relative paths against.
Returns the modified markdown string."
  (let ((re (rx (or "![" "[")
                (group (*? anything))
                "]("
                (group (*? anything))
                ")")))
    (replace-regexp-in-string
     re
     (lambda (match)
       (let* ((label (match-string 1 match))
              (path (match-string 2 match))
              (is-image (string-prefix-p "!" (substring match 0 1)))
              (abs-path (if (file-name-absolute-p path)
                            path
                          (expand-file-name path base-dir))))
         (if (and (not (string-match-p (rx bos (or "http:" "https:")) path))
                  (file-exists-p abs-path))
             (let ((url (save-match-data (textpod--upload-asset abs-path))))
               (if is-image
                   (format "![%s](%s)" label url)
                 (format "[%s](%s)" label url)))
           match)))
     md)))

;;;; Footer

(provide 'textpod)

;;; textpod.el ends here
