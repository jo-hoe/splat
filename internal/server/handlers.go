package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/jo-hoe/splat/internal/imageops"
	"github.com/jo-hoe/splat/internal/source"
)

// thumbCacheEtag is the constant etag passed to the thumbnail cache for v1.
// The cache is keyed per-source-key without invalidation; users clear the
// cache directory to refresh stale thumbnails. Documented in README §11.
const thumbCacheEtag = "v1"

// stripDefaultLimit is the default page size for the strip pagination.
const stripDefaultLimit = 50

// stripMaxLimit clamps user-requested limits to prevent abuse.
const stripMaxLimit = 200

// handleHealthz is the liveness probe.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

// handleIndex renders the full app shell.
func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	data := map[string]any{
		"Title":    "splat",
		"HeightPx": s.cfg.Thumbnails.HeightPx,
	}
	s.render(w, "index.html", data)
}

// stripView is the view-model passed to partial_strip.html.
type stripView struct {
	Entries    []stripEntry
	NextOffset int
	Limit      int
	HasMore    bool
	HeightPx   int
}

// stripEntry is one rendered thumbnail.
type stripEntry struct {
	Key       string
	KeyURL    string
	Basename  string
	HeightPx  int
}

// handleStrip renders one batch of preview thumbnails.
func (s *Server) handleStrip(w http.ResponseWriter, r *http.Request) {
	offset, limit := parseOffsetLimit(r)
	entries, err := s.source.List(r.Context())
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, fmt.Sprintf("list source: %v", err))
		return
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	view := stripView{
		Entries:    make([]stripEntry, 0, end-offset),
		NextOffset: end,
		Limit:      limit,
		HasMore:    end < len(entries),
		HeightPx:   s.cfg.Thumbnails.HeightPx,
	}
	for _, e := range entries[offset:end] {
		view.Entries = append(view.Entries, stripEntry{
			Key:      e.Key,
			KeyURL:   pathEscape(e.Key),
			Basename: path.Base(e.Key),
			HeightPx: s.cfg.Thumbnails.HeightPx,
		})
	}
	s.render(w, "partial_strip.html", view)
}

// parseOffsetLimit reads ?offset and ?limit with defaults and clamping.
func parseOffsetLimit(r *http.Request) (offset, limit int) {
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = stripDefaultLimit
	}
	if limit > stripMaxLimit {
		limit = stripMaxLimit
	}
	return offset, limit
}

// handleThumb serves a cached thumbnail.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	key, ok := s.cleanKeyOr400(w, r)
	if !ok {
		return
	}
	raw, _, status, err := s.fetchOriginal(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	thumb, err := s.thumbs.Get(r.Context(), key, thumbCacheEtag, func(_ context.Context) (image.Image, error) {
		return s.decodeBytes(key, raw)
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("thumbnail: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(thumb)
}

// handleImage serves the original image bytes.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	key, ok := s.cleanKeyOr400(w, r)
	if !ok {
		return
	}
	rc, meta, err := s.source.Get(r.Context(), key)
	if errors.Is(err, source.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rc.Close()
	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = io.Copy(w, rc)
}

// editorView is the view-model passed to partial_editor.html.
type editorView struct {
	Key      string
	KeyURL   string
	Basename string
	Hash     string
	Ratios   []ratio
}

// ratio is a UI ratio preset.
type ratio struct {
	Label string
	Value string // "free", "original", "1:1", ...
}

// presetRatios is the v1 ratio preset list (see DESIGN.md §3.4).
var presetRatios = []ratio{
	{Label: "Free", Value: "free"},
	{Label: "Original", Value: "original"},
	{Label: "1:1", Value: "1:1"},
	{Label: "4:5", Value: "4:5"},
	{Label: "5:4", Value: "5:4"},
	{Label: "4:3", Value: "4:3"},
	{Label: "3:4", Value: "3:4"},
	{Label: "3:2", Value: "3:2"},
	{Label: "2:3", Value: "2:3"},
	{Label: "16:9", Value: "16:9"},
	{Label: "9:16", Value: "9:16"},
}

// handleEditor renders the editor pane for one key.
func (s *Server) handleEditor(w http.ResponseWriter, r *http.Request) {
	key, ok := s.cleanKeyOr400(w, r)
	if !ok {
		return
	}
	raw, _, status, err := s.fetchOriginal(r.Context(), key)
	if err != nil {
		s.renderError(w, status, err.Error())
		return
	}
	hash, _, err := sha256Hex(bytes.NewReader(raw))
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, fmt.Sprintf("hash: %v", err))
		return
	}
	view := editorView{
		Key:      key,
		KeyURL:   pathEscape(key),
		Basename: path.Base(key),
		Hash:     hash,
		Ratios:   presetRatios,
	}
	s.render(w, "partial_editor.html", view)
}

// applyForm captures POST /apply form fields.
type applyForm struct {
	Op   string
	Mode string
	Hash string
	X    int
	Y    int
	W    int
	H    int
}

// handleApply applies a queued operation to a key.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	key, ok := s.cleanKeyOr400(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, http.StatusBadRequest, fmt.Sprintf("form: %v", err))
		return
	}
	form := readApplyForm(r)
	if form.Mode != "inplace" && form.Mode != "copy" {
		s.renderError(w, http.StatusBadRequest, "mode must be 'inplace' or 'copy'")
		return
	}
	op, err := buildOperation(form)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, meta, status, err := s.fetchOriginal(r.Context(), key)
	if err != nil {
		s.renderError(w, status, err.Error())
		return
	}
	currentHash, _, err := sha256Hex(bytes.NewReader(raw))
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, fmt.Sprintf("hash: %v", err))
		return
	}
	if currentHash != form.Hash {
		s.renderConflict(w)
		return
	}
	img, err := s.decodeBytes(key, raw)
	if err != nil {
		s.renderError(w, http.StatusUnsupportedMediaType, fmt.Sprintf("decode: %v", err))
		return
	}
	out, err := op.Apply(img)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, fmt.Sprintf("apply: %v", err))
		return
	}
	target, encoded, contentType, err := s.encodeOutput(r.Context(), key, form.Mode, out)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.source.Put(r.Context(), target, bytes.NewReader(encoded), contentType); err != nil {
		s.renderError(w, http.StatusInternalServerError, fmt.Sprintf("put: %v", err))
		return
	}
	if err := s.maybeDeleteOldKey(r.Context(), key, target, form.Mode); err != nil {
		s.logger.Warn("apply: post-put delete failed", "key", key, "target", target, "err", err)
	}
	s.respondApplySuccess(w, target, meta.ContentType)
}

// readApplyForm extracts the apply form from the request.
func readApplyForm(r *http.Request) applyForm {
	form := applyForm{
		Op:   r.PostForm.Get("op"),
		Mode: r.PostForm.Get("mode"),
		Hash: r.PostForm.Get("hash"),
	}
	form.X, _ = strconv.Atoi(r.PostForm.Get("x"))
	form.Y, _ = strconv.Atoi(r.PostForm.Get("y"))
	form.W, _ = strconv.Atoi(r.PostForm.Get("w"))
	form.H, _ = strconv.Atoi(r.PostForm.Get("h"))
	return form
}

// buildOperation constructs an Operation from the form's op field.
func buildOperation(f applyForm) (imageops.Operation, error) {
	switch f.Op {
	case imageops.Crop{}.Name():
		return imageops.Crop{X: f.X, Y: f.Y, W: f.W, H: f.H}, nil
	case imageops.RotateCW90{}.Name():
		return imageops.RotateCW90{}, nil
	case imageops.RotateCCW90{}.Name():
		return imageops.RotateCCW90{}, nil
	case imageops.Rotate180{}.Name():
		return imageops.Rotate180{}, nil
	case imageops.FlipHorizontal{}.Name():
		return imageops.FlipHorizontal{}, nil
	case imageops.FlipVertical{}.Name():
		return imageops.FlipVertical{}, nil
	default:
		return nil, fmt.Errorf("unknown op %q", f.Op)
	}
}

// encodeOutput resolves the target key and encodes the output image bytes.
// Returns target key, encoded bytes, content-type, and any error.
func (s *Server) encodeOutput(ctx context.Context, srcKey, mode string, img image.Image) (string, []byte, string, error) {
	inputExt := strings.ToLower(path.Ext(srcKey))
	outFmt, outExt, ok := s.registry.OutputFor(inputExt)
	if !ok {
		return "", nil, "", fmt.Errorf("unsupported extension %q", inputExt)
	}
	target, err := s.computeTargetKey(ctx, srcKey, outExt, mode)
	if err != nil {
		return "", nil, "", err
	}
	var buf bytes.Buffer
	if err := outFmt.Encode(&buf, img); err != nil {
		return "", nil, "", fmt.Errorf("encode: %w", err)
	}
	return target, buf.Bytes(), outFmt.ContentType, nil
}

// computeTargetKey resolves the destination key for the apply operation.
func (s *Server) computeTargetKey(ctx context.Context, srcKey, outExt, mode string) (string, error) {
	if mode == "copy" {
		return source.NextCopyKey(ctx, srcKey, outExt, s.cfg.Editing.CopySuffix, s.source.Exists)
	}
	// inplace
	currentExt := strings.ToLower(path.Ext(srcKey))
	if currentExt == outExt {
		return srcKey, nil
	}
	stem := strings.TrimSuffix(srcKey, path.Ext(srcKey))
	return stem + outExt, nil
}

// maybeDeleteOldKey deletes the original after a successful in-place save
// when the output extension changed (webp → png).
func (s *Server) maybeDeleteOldKey(ctx context.Context, srcKey, target, mode string) error {
	if mode != "inplace" || srcKey == target {
		return nil
	}
	return s.source.Delete(ctx, srcKey)
}

// handleDelete removes a key.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	key, ok := s.cleanKeyOr400(w, r)
	if !ok {
		return
	}
	if err := s.source.Delete(r.Context(), key); err != nil {
		if errors.Is(err, source.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setToastTrigger(w, fmt.Sprintf("Deleted %s", path.Base(key)), "success")
	w.WriteHeader(http.StatusOK)
}

// cleanKeyOr400 cleans the {key...} path value or writes a 400 response.
func (s *Server) cleanKeyOr400(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := r.PathValue("key")
	key, err := source.CleanKey(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	return key, true
}

// fetchOriginal reads the full original bytes plus metadata, mapping
// ErrNotFound to a 404 status. Returns (raw, meta, httpStatus, err); err
// is non-nil iff httpStatus >= 400.
func (s *Server) fetchOriginal(ctx context.Context, key string) ([]byte, source.Metadata, int, error) {
	rc, meta, err := s.source.Get(ctx, key)
	if errors.Is(err, source.ErrNotFound) {
		return nil, meta, http.StatusNotFound, fmt.Errorf("not found: %s", key)
	}
	if err != nil {
		return nil, meta, http.StatusInternalServerError, fmt.Errorf("get: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, meta, http.StatusInternalServerError, fmt.Errorf("read: %w", err)
	}
	return raw, meta, http.StatusOK, nil
}

// decodeBytes decodes raw image bytes via the registry, dispatching by ext.
func (s *Server) decodeBytes(key string, raw []byte) (image.Image, error) {
	ext := strings.ToLower(path.Ext(key))
	f, ok := s.registry.ByExt(ext)
	if !ok {
		return nil, fmt.Errorf("unsupported extension %q", ext)
	}
	if f.Decode == nil {
		return nil, fmt.Errorf("no decoder for %q", ext)
	}
	return f.Decode(bytes.NewReader(raw))
}

// render writes a template to w as text/html, logging any error.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("template execute", "name", name, "err", err)
	}
}

// renderError renders the error fragment with HTTP 200 (htmx swaps body
// regardless on 200) and a request-attached note. The status arg is logged
// only; htmx clients see status 200 to allow the inline alert to swap in.
func (s *Server) renderError(w http.ResponseWriter, status int, msg string) {
	s.logger.Warn("handler error", "status", status, "msg", msg)
	view := map[string]any{"Message": msg}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := s.templates.ExecuteTemplate(w, "partial_error.html", view); err != nil {
		s.logger.Error("template execute partial_error", "err", err)
	}
}

// renderConflict renders a 409 with the same error fragment plus a clear
// "reload required" message.
func (s *Server) renderConflict(w http.ResponseWriter) {
	view := map[string]any{
		"Message":      "This file was modified elsewhere. Reload to continue.",
		"NeedsReload":  true,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	if err := s.templates.ExecuteTemplate(w, "partial_error.html", view); err != nil {
		s.logger.Error("template execute partial_error", "err", err)
	}
}

// respondApplySuccess writes the success fragment plus a toast trigger.
func (s *Server) respondApplySuccess(w http.ResponseWriter, target, _ string) {
	setToastTrigger(w, fmt.Sprintf("Saved %s", target), "success")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := map[string]any{"Target": target}
	if err := s.templates.ExecuteTemplate(w, "partial_apply_success.html", view); err != nil {
		s.logger.Error("template execute partial_apply_success", "err", err)
	}
}

// setToastTrigger sets the HX-Trigger header for client-side toast display.
func setToastTrigger(w http.ResponseWriter, message, kind string) {
	payload := map[string]map[string]string{
		"showToast": {"message": message, "kind": kind},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	w.Header().Set("HX-Trigger", string(b))
}
