package adminapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"
)

type AuthSubject struct {
	Username    string
	StaticToken bool
}

type authContextKey struct{}

type Authenticator struct {
	admins       map[string]string
	staticTokens map[string]struct{}
	tokenSecret  []byte
	tokenTTL     time.Duration
	now          func() time.Time
}

type loginTokenClaims struct {
	Username string `json:"username"`
	Expires  int64  `json:"exp"`
}

var (
	errUnauthorized = errors.New("unauthorized")
	errTokenExpired = errors.New("token expired")
)

func NewAuthenticator(options option.AdminAPIServiceOptions) (*Authenticator, error) {
	admins := make(map[string]string, len(options.Admins))
	for _, admin := range options.Admins {
		admins[admin.Username] = admin.Password
	}
	staticTokens := make(map[string]struct{}, len(options.Tokens))
	for _, token := range options.Tokens {
		if token == "" {
			continue
		}
		staticTokens[token] = struct{}{}
	}
	return &Authenticator{
		admins:       admins,
		staticTokens: staticTokens,
		tokenSecret:  []byte(options.TokenSecret),
		tokenTTL:     time.Duration(options.TokenTTL),
		now:          time.Now,
	}, nil
}

func SubjectFromContext(ctx context.Context) (AuthSubject, bool) {
	subject, ok := ctx.Value(authContextKey{}).(AuthSubject)
	return subject, ok
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token := extractBearerToken(request.Header.Get("Authorization"))
		subject, err := a.validateBearerToken(token)
		if err != nil {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, subject))
		next.ServeHTTP(writer, request)
	})
}

func (a *Authenticator) issueLoginToken(username string) (string, error) {
	if len(a.tokenSecret) == 0 || a.tokenTTL <= 0 {
		return "", errUnauthorized
	}
	claims := loginTokenClaims{
		Username: username,
		Expires:  a.now().Add(a.tokenTTL).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadEncoded := base64.RawURLEncoding.EncodeToString(payload)
	signatureEncoded := base64.RawURLEncoding.EncodeToString(a.signToken(payloadEncoded))
	return payloadEncoded + "." + signatureEncoded, nil
}

func (a *Authenticator) validateBearerToken(token string) (AuthSubject, error) {
	if token == "" {
		return AuthSubject{}, errUnauthorized
	}
	if _, ok := a.staticTokens[token]; ok {
		return AuthSubject{StaticToken: true}, nil
	}
	if len(a.tokenSecret) == 0 {
		return AuthSubject{}, errUnauthorized
	}
	payloadEncoded, signatureEncoded, ok := strings.Cut(token, ".")
	if !ok || payloadEncoded == "" || signatureEncoded == "" {
		return AuthSubject{}, errUnauthorized
	}
	signature, err := base64.RawURLEncoding.DecodeString(signatureEncoded)
	if err != nil {
		return AuthSubject{}, errUnauthorized
	}
	expectedSignature := a.signToken(payloadEncoded)
	if !hmac.Equal(signature, expectedSignature) {
		return AuthSubject{}, errUnauthorized
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadEncoded)
	if err != nil {
		return AuthSubject{}, errUnauthorized
	}
	var claims loginTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return AuthSubject{}, errUnauthorized
	}
	if claims.Username == "" || a.now().Unix() >= claims.Expires {
		return AuthSubject{}, errTokenExpired
	}
	return AuthSubject{Username: claims.Username}, nil
}

func (a *Authenticator) signToken(payload string) []byte {
	mac := hmac.New(sha256.New, a.tokenSecret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func extractBearerToken(authorization string) string {
	if len(authorization) < len("Bearer ") || !strings.EqualFold(authorization[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(authorization[7:])
}
