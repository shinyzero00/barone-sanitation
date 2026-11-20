package backend

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// Huma only traverses exported embedded fields when discovering parameters and headers.
type HTTPBody[T any] struct {
	Body T
}

type httpOutput[T any] struct {
	Body T
}

func httpResult[T any](body T, err error) (*httpOutput[T], error) {
	if err != nil {
		return nil, err
	}
	return &httpOutput[T]{Body: body}, nil
}

type HTTPIDPath struct {
	ID int64 `path:"id" minimum:"1"`
}

type HTTPConsultantPath struct {
	ConsultantID int64 `path:"consultant_id" minimum:"1"`
}

type httpIDBody[T any] struct {
	HTTPIDPath
	HTTPBody[T]
}

type HTTPOptionalBody struct {
	Body *struct{} `required:"false" nullable:"false"`
}

type HTTPCookieOutput struct {
	Cookies []http.Cookie `header:"Set-Cookie"`
}

type HTTPOtpTiming struct {
	ExpiresAt string `header:"OTP-Expires-At" format:"date-time" doc:"When the code expires (RFC 3339 UTC)."`
	ResendAt  string `header:"OTP-Resend-At" format:"date-time" doc:"Earliest resend time (RFC 3339 UTC); a later 429 Retry-After takes precedence."`
}

func httpOTPTiming(t codeTiming) HTTPOtpTiming {
	return HTTPOtpTiming{ExpiresAt: t.ExpiresAt.UTC().Format(time.RFC3339Nano), ResendAt: t.ResendAt.UTC().Format(time.RFC3339Nano)}
}

type HTTPTimeRange struct {
	From time.Time `query:"from" required:"true"`
	To   time.Time `query:"to" required:"true"`
}

func operation(method, path, id, summary, tag string, status int) huma.Operation {
	return huma.Operation{
		Method: method, Path: path, OperationID: id,
		Summary: summary, Tags: []string{tag}, DefaultStatus: status,
	}
}

func registerHTTPOperations(api huma.API, s *Service) {
	type health struct {
		Status string `json:"status"`
	}
	httpRegister(api, operation("GET", "/healthz", "getHealth", "Check database health", "Health", 200), false,
		func(ctx context.Context, _ *struct{}) (*httpOutput[health], error) {
			if err := s.DB.PingContext(ctx); err != nil {
				return nil, problem(503, "unhealthy")
			}
			return httpResult(health{Status: "ok"}, nil)
		})
	type publicConfig struct {
		PriceCurrency string `json:"price_currency" pattern:"^([A-Z]{3})?$" doc:"Deployment-wide display currency; empty if not configured. Does not convert prices."`
	}
	httpRegister(api, operation("GET", "/api/v1/config", "getPublicConfig", "Get public application settings", "Configuration", 200), false,
		func(_ context.Context, _ *struct{}) (*httpOutput[publicConfig], error) {
			return httpResult(publicConfig{PriceCurrency: s.Config.PriceCurrency}, nil)
		})

	type csrfInput struct {
		CSRF string `cookie:"__Host-csrf" required:"false"`
	}
	type csrfBody struct {
		Token string `json:"csrf_token"`
	}
	type csrfOutput struct {
		HTTPCookieOutput
		Body csrfBody
	}
	csrf := func(ctx context.Context, in *csrfInput) (*csrfOutput, error) {
		token, cookies, err := httpCSRFToken(ctx, s, in.CSRF)
		if err != nil {
			return nil, err
		}
		return &csrfOutput{HTTPCookieOutput: HTTPCookieOutput{Cookies: cookies}, Body: csrfBody{Token: token}}, nil
	}
	httpRegister(api, operation("GET", "/csrf", "getCSRF", "Get CSRF token", "Authentication", 200), false, csrf)
	httpRegister(api, operation("GET", "/api/v1/auth/csrf", "getAuthCSRF", "Get CSRF token", "Authentication", 200), false, csrf)

	type registration struct {
		UserID int64 `json:"user_id"`
	}
	type registrationOutput struct {
		HTTPOtpTiming
		Body registration
	}
	httpRegister(api, operation("POST", "/api/v1/auth/register", "registerClient", "Register client", "Authentication", 201), false,
		func(ctx context.Context, in *HTTPBody[RegisterInput]) (*registrationOutput, error) {
			id, timing, err := s.register(ctx, in.Body, httpIP(ctx))
			if err != nil {
				return nil, err
			}
			return &registrationOutput{HTTPOtpTiming: httpOTPTiming(timing), Body: registration{UserID: id}}, nil
		})
	type requestCode struct {
		Email   string `json:"email"`
		Purpose string `json:"purpose" enum:"verify_email,reset_password"`
	}
	httpRegister(api, operation("POST", "/api/v1/auth/codes", "requestCode", "Request verification code", "Authentication", 204), false,
		func(ctx context.Context, in *HTTPBody[requestCode]) (*HTTPOtpTiming, error) {
			timing, err := s.requestCode(ctx, in.Body.Email, in.Body.Purpose, httpIP(ctx))
			if err != nil {
				return nil, err
			}
			out := httpOTPTiming(timing)
			return &out, nil
		})
	httpRegister(api, operation("POST", "/api/v1/auth/verify-email", "verifyEmail", "Verify email address", "Authentication", 204), false,
		func(ctx context.Context, in *HTTPBody[CodeInput]) (*struct{}, error) {
			return nil, s.VerifyEmail(ctx, in.Body)
		})
	type login struct {
		Email    string `json:"email"`
		Password string `json:"password" writeOnly:"true"`
	}
	type loginOutput struct {
		HTTPOtpTiming
		HTTPCookieOutput
	}
	httpRegister(api, operation("POST", "/api/v1/auth/login", "startLogin", "Start login challenge", "Authentication", 204), false,
		func(ctx context.Context, in *HTTPBody[login]) (*loginOutput, error) {
			token, timing, err := s.startLogin(ctx, in.Body.Email, in.Body.Password, httpIP(ctx))
			if err != nil {
				return nil, err
			}
			return &loginOutput{HTTPOtpTiming: httpOTPTiming(timing), HTTPCookieOutput: HTTPCookieOutput{Cookies: []http.Cookie{
				httpCookie(challengeCookie, token, s.Config.CodeTTL, true),
			}}}, nil
		})
	type verifyLogin struct {
		HTTPBody[CodeInput]
		Challenge string `cookie:"__Host-login-challenge" required:"false"`
	}
	verifyLoginOp := operation("POST", "/api/v1/auth/login/verify", "finishLogin", "Complete login challenge", "Authentication", 204)
	verifyLoginOp.Security = []map[string][]string{{"LoginChallenge": {}}}
	httpRegister(api, verifyLoginOp, false,
		func(ctx context.Context, in *verifyLogin) (*HTTPCookieOutput, error) {
			token, err := s.FinishLogin(ctx, in.Body, in.Challenge)
			if err != nil {
				return nil, err
			}
			return &HTTPCookieOutput{Cookies: []http.Cookie{
				httpCookie(sessionCookie, token, s.Config.SessionTTL, true),
				httpCookie(challengeCookie, "", -time.Second, true),
			}}, nil
		})
	type resetPassword struct {
		Email    string `json:"email"`
		Code     string `json:"code" pattern:"^[0-9]{6}$" writeOnly:"true"`
		Password string `json:"password" writeOnly:"true" doc:"New password, 12-128 bytes (validated by the service)."`
	}
	httpRegister(api, operation("POST", "/api/v1/auth/reset-password", "resetPassword", "Reset password", "Authentication", 204), false,
		func(ctx context.Context, in *HTTPBody[resetPassword]) (*struct{}, error) {
			return nil, s.ResetPassword(ctx, CodeInput{Email: in.Body.Email, Code: in.Body.Code}, in.Body.Password)
		})
	httpRegister(api, operation("POST", "/api/v1/auth/logout", "logout", "End current session", "Authentication", 204), true,
		func(ctx context.Context, _ *HTTPOptionalBody) (*HTTPCookieOutput, error) {
			if err := s.Logout(ctx, httpActor(ctx)); err != nil {
				return nil, err
			}
			return &HTTPCookieOutput{Cookies: []http.Cookie{
				httpCookie(sessionCookie, "", -time.Second, true),
			}}, nil
		})

	httpRegister(api, operation("GET", "/api/v1/me", "getProfile", "Get current profile", "Profile", 200), true,
		func(ctx context.Context, _ *struct{}) (*httpOutput[Profile], error) {
			return httpResult(s.GetProfile(ctx, httpActor(ctx)))
		})
	httpRegister(api, operation("PATCH", "/api/v1/me", "updateProfile", "Update current profile", "Profile", 200), true,
		func(ctx context.Context, in *HTTPBody[ProfileUpdate]) (*httpOutput[Profile], error) {
			return httpResult(s.UpdateProfile(ctx, httpActor(ctx), in.Body))
		})
	httpRegister(api, operation("GET", "/api/v1/me/offerings", "listMyOfferings", "List assigned consultant offerings", "Profile", 200), true,
		func(ctx context.Context, _ *struct{}) (*httpOutput[[]AssignedOffering], error) {
			return httpResult(s.ListMyOfferings(ctx, httpActor(ctx)))
		})
	httpRegister(api, operation("GET", "/api/v1/addresses", "listAddresses", "List owned addresses", "Addresses", 200), true,
		func(ctx context.Context, _ *struct{}) (*httpOutput[[]Address], error) {
			return httpResult(s.ListAddresses(ctx, httpActor(ctx)))
		})
	httpRegister(api, operation("GET", "/api/v1/addresses/{id}", "getAddress", "Get owned address", "Addresses", 200), true,
		func(ctx context.Context, in *HTTPIDPath) (*httpOutput[Address], error) {
			return httpResult(s.GetAddress(ctx, httpActor(ctx), in.ID))
		})
	httpRegister(api, operation("POST", "/api/v1/addresses", "createAddress", "Create address", "Addresses", 201), true,
		func(ctx context.Context, in *HTTPBody[AddressInput]) (*httpOutput[Address], error) {
			return httpResult(s.SaveAddress(ctx, httpActor(ctx), 0, in.Body))
		})
	httpRegister(api, operation("PUT", "/api/v1/addresses/{id}", "replaceAddress", "Replace owned address", "Addresses", 200), true,
		func(ctx context.Context, in *httpIDBody[AddressInput]) (*httpOutput[Address], error) {
			return httpResult(s.SaveAddress(ctx, httpActor(ctx), in.ID, in.Body))
		})
	type deleteByID struct {
		HTTPIDPath
		HTTPOptionalBody
	}
	httpRegister(api, operation("DELETE", "/api/v1/addresses/{id}", "deleteAddress", "Delete owned address", "Addresses", 204), true,
		func(ctx context.Context, in *deleteByID) (*struct{}, error) {
			return nil, s.DeleteAddress(ctx, httpActor(ctx), in.ID)
		})

	httpRegister(api, operation("GET", "/api/v1/consultants", "listConsultants", "List active consultants", "Catalog", 200), false,
		func(ctx context.Context, _ *struct{}) (*httpOutput[[]Consultant], error) {
			return httpResult(s.ListConsultants(ctx))
		})
	httpRegister(api, operation("GET", "/api/v1/consultation-types", "listConsultationTypes", "List active consultation types", "Catalog", 200), false,
		func(ctx context.Context, _ *struct{}) (*httpOutput[[]ConsultationType], error) {
			return httpResult(s.ListConsultationTypes(ctx))
		})
	httpRegister(api, operation("GET", "/api/v1/offerings", "listOfferings", "List active offerings", "Catalog", 200), false,
		func(ctx context.Context, _ *struct{}) (*httpOutput[[]Offering], error) {
			return httpResult(s.ListOfferings(ctx))
		})
	type page struct {
		Limit  int `query:"limit" default:"100" minimum:"1" maximum:"1000"`
		Offset int `query:"offset" default:"0" minimum:"0"`
	}
	httpRegister(api, operation("GET", "/api/v1/admin/consultants", "listAdminConsultants", "List all consultants", "Administration", 200), true,
		func(ctx context.Context, in *page) (*httpOutput[[]Consultant], error) {
			return httpResult(s.ListAdminConsultants(ctx, httpActor(ctx), in.Limit, in.Offset))
		})
	httpRegister(api, operation("GET", "/api/v1/admin/consultation-types", "listAdminConsultationTypes", "List all consultation type versions", "Administration", 200), true,
		func(ctx context.Context, in *page) (*httpOutput[[]ConsultationType], error) {
			return httpResult(s.ListAdminConsultationTypes(ctx, httpActor(ctx), in.Limit, in.Offset))
		})
	httpRegister(api, operation("GET", "/api/v1/admin/offerings", "listAdminOfferings", "List all offerings", "Administration", 200), true,
		func(ctx context.Context, in *page) (*httpOutput[[]Offering], error) {
			return httpResult(s.ListAdminOfferings(ctx, httpActor(ctx), in.Limit, in.Offset))
		})
	type candidateSearch struct {
		Email string `query:"email" required:"true" maxLength:"254"`
	}
	httpRegister(api, operation("GET", "/api/v1/admin/consultant-candidates", "findConsultantCandidate", "Find an existing client by email", "Administration", 200), true,
		func(ctx context.Context, in *candidateSearch) (*httpOutput[ConsultantCandidate], error) {
			return httpResult(s.FindConsultantCandidate(ctx, httpActor(ctx), in.Email))
		})
	type provisionConsultant struct {
		UserID int64  `json:"user_id" minimum:"1"`
		Bio    string `json:"bio" required:"false" nullable:"true" maxLength:"10000"`
	}
	httpRegister(api, operation("POST", "/api/v1/admin/consultants", "provisionConsultant", "Provision consultant", "Administration", 201), true,
		func(ctx context.Context, in *HTTPBody[provisionConsultant]) (*httpOutput[Consultant], error) {
			return httpResult(s.ProvisionConsultant(ctx, httpActor(ctx), in.Body.UserID, in.Body.Bio))
		})
	type activation struct {
		Active bool `json:"is_active"`
	}
	httpRegister(api, operation("PATCH", "/api/v1/admin/consultants/{id}", "setConsultantActive", "Set consultant activation", "Administration", 204), true,
		func(ctx context.Context, in *httpIDBody[activation]) (*struct{}, error) {
			return nil, s.SetConsultantActive(ctx, httpActor(ctx), in.ID, in.Body.Active)
		})
	type consultantBio struct {
		Bio string `json:"bio" maxLength:"10000"`
	}
	httpRegister(api, operation("PUT", "/api/v1/admin/consultants/{id}/bio", "updateConsultantBio", "Replace consultant bio", "Administration", 204), true,
		func(ctx context.Context, in *httpIDBody[consultantBio]) (*struct{}, error) {
			return nil, s.UpdateConsultantBio(ctx, httpActor(ctx), in.ID, in.Body.Bio)
		})
	httpRegister(api, operation("POST", "/api/v1/admin/consultation-types", "createConsultationType", "Create consultation type", "Administration", 201), true,
		func(ctx context.Context, in *HTTPBody[ConsultationTypeInput]) (*httpOutput[ConsultationType], error) {
			return httpResult(s.ReplaceConsultationType(ctx, httpActor(ctx), 0, in.Body))
		})
	httpRegister(api, operation("PUT", "/api/v1/admin/consultation-types/{id}", "replaceConsultationType", "Replace consultation type", "Administration", 201), true,
		func(ctx context.Context, in *httpIDBody[ConsultationTypeInput]) (*httpOutput[ConsultationType], error) {
			return httpResult(s.ReplaceConsultationType(ctx, httpActor(ctx), in.ID, in.Body))
		})
	httpRegister(api, operation("DELETE", "/api/v1/admin/consultation-types/{id}", "deactivateConsultationType", "Permanently deactivate consultation type and offerings", "Administration", 204), true,
		func(ctx context.Context, in *deleteByID) (*struct{}, error) {
			return nil, s.DeactivateConsultationType(ctx, httpActor(ctx), in.ID)
		})
	type setOffering struct {
		TypeID       int64 `json:"consultation_type_id" minimum:"1"`
		ConsultantID int64 `json:"consultant_id" minimum:"1"`
		Active       bool  `json:"is_active"`
	}
	httpRegister(api, operation("PUT", "/api/v1/admin/offerings", "setOffering", "Set offering activation", "Administration", 200), true,
		func(ctx context.Context, in *HTTPBody[setOffering]) (*httpOutput[Offering], error) {
			return httpResult(s.SetOffering(ctx, httpActor(ctx), in.Body.TypeID, in.Body.ConsultantID, in.Body.Active))
		})

	const intervals = "/api/v1/consultants/{consultant_id}/work-intervals"
	type IntervalPath struct {
		HTTPConsultantPath
		HTTPIDPath
	}
	type createInterval struct {
		HTTPConsultantPath
		HTTPBody[WorkIntervalInput]
	}
	type updateInterval struct {
		IntervalPath
		HTTPBody[WorkIntervalInput]
	}
	type deleteInterval struct {
		IntervalPath
		HTTPOptionalBody
	}
	httpRegister(api, operation("GET", intervals, "listWorkIntervals", "List work intervals", "Schedule", 200), true,
		func(ctx context.Context, in *HTTPConsultantPath) (*httpOutput[[]WorkInterval], error) {
			return httpResult(s.ListWorkIntervals(ctx, httpActor(ctx), in.ConsultantID))
		})
	httpRegister(api, operation("GET", intervals+"/{id}", "getWorkInterval", "Get work interval", "Schedule", 200), true,
		func(ctx context.Context, in *IntervalPath) (*httpOutput[WorkInterval], error) {
			return httpResult(s.GetWorkInterval(ctx, httpActor(ctx), in.ConsultantID, in.ID))
		})
	httpRegister(api, operation("POST", intervals, "createWorkInterval", "Create work interval", "Schedule", 201), true,
		func(ctx context.Context, in *createInterval) (*httpOutput[WorkInterval], error) {
			return httpResult(s.SaveWorkInterval(ctx, httpActor(ctx), in.ConsultantID, 0, in.Body))
		})
	httpRegister(api, operation("PUT", intervals+"/{id}", "replaceWorkInterval", "Replace work interval", "Schedule", 200), true,
		func(ctx context.Context, in *updateInterval) (*httpOutput[WorkInterval], error) {
			return httpResult(s.SaveWorkInterval(ctx, httpActor(ctx), in.ConsultantID, in.ID, in.Body))
		})
	httpRegister(api, operation("DELETE", intervals+"/{id}", "deleteWorkInterval", "Delete work interval", "Schedule", 204), true,
		func(ctx context.Context, in *deleteInterval) (*struct{}, error) {
			return nil, s.DeleteWorkInterval(ctx, httpActor(ctx), in.ConsultantID, in.ID)
		})
	type slotsInput struct {
		HTTPIDPath
		HTTPTimeRange
	}
	httpRegister(api, operation("GET", "/api/v1/offerings/{id}/slots", "listAvailableSlots", "List available slots", "Schedule", 200), false,
		func(ctx context.Context, in *slotsInput) (*httpOutput[[]Slot], error) {
			return httpResult(s.AvailableSlots(ctx, in.ID, in.From, in.To))
		})

	type listBookings struct {
		HTTPTimeRange
		Limit  int `query:"limit" default:"100" minimum:"1" maximum:"1000"`
		Offset int `query:"offset" default:"0" minimum:"0"`
	}
	httpRegister(api, operation("GET", "/api/v1/bookings", "listBookings", "List accessible bookings", "Bookings", 200), true,
		func(ctx context.Context, in *listBookings) (*httpOutput[[]Booking], error) {
			return httpResult(s.ListBookings(ctx, httpActor(ctx), in.From, in.To, in.Limit, in.Offset))
		})
	httpRegister(api, operation("POST", "/api/v1/bookings", "createBooking", "Create booking", "Bookings", 201), true,
		func(ctx context.Context, in *HTTPBody[BookingInput]) (*httpOutput[Booking], error) {
			return httpResult(s.CreateBooking(ctx, httpActor(ctx), in.Body))
		})
	httpRegister(api, operation("GET", "/api/v1/bookings/{id}", "getBooking", "Get accessible booking", "Bookings", 200), true,
		func(ctx context.Context, in *HTTPIDPath) (*httpOutput[Booking], error) {
			return httpResult(s.GetBooking(ctx, httpActor(ctx), in.ID))
		})
	httpRegister(api, operation("GET", "/api/v1/bookings/{id}/events", "listBookingEvents", "List booking status events", "Bookings", 200), true,
		func(ctx context.Context, in *HTTPIDPath) (*httpOutput[[]BookingEvent], error) {
			return httpResult(s.ListBookingEvents(ctx, httpActor(ctx), in.ID))
		})
	type statusChange struct {
		Status string  `json:"status" enum:"confirmed,cancelled,completed,no_show"`
		Reason *string `json:"reason" required:"false" maxLength:"2000"`
	}
	httpRegister(api, operation("POST", "/api/v1/bookings/{id}/status", "changeBookingStatus", "Change booking status", "Bookings", 200), true,
		func(ctx context.Context, in *httpIDBody[statusChange]) (*httpOutput[Booking], error) {
			return httpResult(s.ChangeBookingStatus(ctx, httpActor(ctx), in.ID, in.Body.Status, in.Body.Reason))
		})
	type consultationResult struct {
		HTTPBody[ConsultationResultInput]
	}
	httpRegister(api, operation("POST", "/api/v1/bookings/{id}/result", "createConsultationResult", "Store consultation result", "Bookings", 201), true,
		func(ctx context.Context, in *httpIDBody[ConsultationResultInput]) (*httpOutput[ConsultationResult], error) {
			return httpResult(s.CreateConsultationResult(ctx, httpActor(ctx), in.ID, in.Body))
		})
	httpRegister(api, operation("GET", "/api/v1/bookings/{id}/result", "getConsultationResult", "Get consultation result", "Bookings", 200), true,
		func(ctx context.Context, in *HTTPIDPath) (*httpOutput[ConsultationResult], error) {
			return httpResult(s.GetConsultationResult(ctx, httpActor(ctx), in.ID))
		})
}
