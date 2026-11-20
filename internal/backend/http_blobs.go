package backend

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

const avatarUploadPath = "/api/v1/me/avatar"
const avatarUploadType = "application/octet-stream"

func registerBlobOperations(api huma.API, s *Service) {
	blobs := BlobService{Service: s}
	type blobInput struct {
		HTTPIDPath
		Session string `cookie:"__Host-session"`
	}
	type blobOutput struct {
		ContentType string `header:"Content-Type"`
		Body        []byte
	}
	op := operation("GET", "/api/v1/blobs/{id}", "getBlob", "Read an accessible avatar", "Avatars", 200)
	op.Responses = map[string]*huma.Response{"200": {Description: "Avatar image", Content: map[string]*huma.MediaType{}}}
	for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp"} {
		op.Responses["200"].Content[mediaType] = &huma.MediaType{Schema: &huma.Schema{Type: "string", Format: "binary"}}
	}
	httpRegister(api, op, false, func(ctx context.Context, in *blobInput) (*blobOutput, error) {
		var actor Actor
		if in.Session != "" {
			var err error
			actor, err = s.Authenticate(ctx, in.Session)
			var apiErr *APIError
			if err != nil && (!errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized) {
				return nil, err
			}
		}
		blob, err := blobs.get(ctx, in.ID, actor)
		return &blobOutput{ContentType: blob.ContentType, Body: blob.Data}, err
	})
	type uploadInput struct {
		RawBody []byte `contentType:"application/octet-stream"`
	}
	upload := operation("PUT", avatarUploadPath, "replaceMyAvatar", "Replace your client avatar (up to 2 MiB)", "Avatars", 204)
	upload.MaxBodyBytes = AvatarMaxBytes
	httpRegister(api, upload, true, func(ctx context.Context, in *uploadInput) (*struct{}, error) {
		if len(in.RawBody) == 0 {
			return nil, problem(422, "invalid_avatar")
		}
		return nil, blobs.replaceClientAvatar(ctx, httpActor(ctx), in.RawBody)
	})
	httpRegister(api, operation("DELETE", avatarUploadPath, "deleteMyAvatar", "Remove your client avatar", "Avatars", 204), true,
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			return nil, blobs.replaceClientAvatar(ctx, httpActor(ctx), nil)
		})
}
