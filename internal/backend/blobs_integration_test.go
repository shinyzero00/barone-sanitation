//go:build integration

package backend

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"testing"
)

func TestHTTPAvatarAccessAndReplacement(t *testing.T) {
	s, mail := authFixture(t)
	s.Config.PublicOrigin = httpTestOrigin
	app := NewApp(s)
	owner, other, admin, guest := newHTTPBrowser(t, app), newHTTPBrowser(t, app), newHTTPBrowser(t, app), newHTTPBrowser(t, app)
	owner.registerLogin(t, mail, "owner@avatar.test")
	consultantID := other.registerLogin(t, mail, "other@avatar.test")
	adminID := admin.registerLogin(t, mail, "admin@avatar.test")
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE users SET role='admin' WHERE user_id=$1`, adminID); err != nil {
		t.Fatal(err)
	}
	var c Consultant
	admin.request(t, "POST", "/api/v1/admin/consultants", map[string]any{"user_id": consultantID}, 201, &c)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	upload := func(b *httpTestBrowser, body, mediaType string, status int) {
		t.Helper()
		req := httpUnsafeRequest("PUT", avatarUploadPath, body, b.csrf)
		req.Header.Set("Content-Type", mediaType)
		for _, cookie := range b.cookies {
			req.AddCookie(cookie)
		}
		httpDo(t, app, req, status)
	}
	checkImage := func(b *httpTestBrowser, path string) {
		t.Helper()
		r := b.request(t, "GET", path, nil, 200, nil)
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, data.Bytes()) || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("unexpected image response")
		}
	}
	upload(guest, data.String(), avatarUploadType, 401)
	upload(other, data.String(), avatarUploadType, 403)
	upload(owner, data.String(), "image/png", 415)
	upload(owner, "not an image", avatarUploadType, 422)
	upload(owner, data.String(), avatarUploadType, 204)
	var p Profile
	owner.request(t, "GET", "/api/v1/me", nil, 200, &p)
	if p.AvatarURL == nil {
		t.Fatal("missing avatar link")
	}
	first := *p.AvatarURL
	checkImage(owner, first)
	checkImage(admin, first)
	guest.request(t, "GET", first, nil, 404, nil)
	other.request(t, "GET", first, nil, 404, nil)
	upload(owner, data.String(), avatarUploadType, 204)
	owner.request(t, "GET", first, nil, 404, nil)
	owner.request(t, "GET", "/api/v1/me", nil, 200, &p)
	if p.AvatarURL == nil || *p.AvatarURL == first {
		t.Fatal("avatar not replaced")
	}
	owner.request(t, "DELETE", avatarUploadPath, nil, 204, nil)
	admin.request(t, "GET", *p.AvatarURL, nil, 404, nil)
	var count int
	if err := s.DB.GetContext(t.Context(), &count, `SELECT count(*) FROM blobs`); err != nil || count != 0 {
		t.Fatalf("leftover blobs: %d, %v", count, err)
	}
	tx, err := s.DB.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := (BlobService{}).SetConsultantAvatar(t.Context(), tx, c.ID, data.Bytes(), false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var consultants []Consultant
	guest.request(t, "GET", "/api/v1/consultants", nil, 200, &consultants)
	if len(consultants) != 1 || consultants[0].AvatarURL == nil {
		t.Fatal("missing public avatar link")
	}
	checkImage(guest, *consultants[0].AvatarURL)
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE consultants SET is_active=false WHERE consultant_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	guest.request(t, "GET", *consultants[0].AvatarURL, nil, 404, nil)
}
