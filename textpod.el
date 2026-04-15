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
  (let ((id (org-entry-get nil "TEXTPOD_ID")))
    (unless id
      (user-error "No TEXTPOD_ID property on this heading"))
    (browse-url (format "%s/note/%s" textpod-url id))))

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
         (org-text (textpod--process-details org-text))
         (md (let ((org-export-with-toc nil)
                   (org-export-with-todo-keywords t)
                   (org-md-headline-style 'atx))
               (org-export-string-as org-text 'md t)))
         (md (textpod--wrap-details md))
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

;;;; Footer

(provide 'textpod)

;;; textpod.el ends here
