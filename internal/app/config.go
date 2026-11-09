package app

import (
	"os"
	"strings"
)

// Config replica los valores de mysite/settings.py. Las variables de entorno
// tienen los mismos nombres que en el .env de Django.
type Config struct {
	Addr      string
	SecretKey string

	DBHost, DBPort, DBName, DBUser, DBPass string
	DBSSLMode                              string

	JWTCookieSecure   bool
	JWTCookieSameSite string
	JWTCookieDomain   string

	PasswordLoginEnabled bool

	IAMVerifyLogin     bool
	IAMMeURL           string
	IAMLocalBypass     bool
	IAMLocalBypassHost []string

	MailjetAPIKey      string
	MailjetAPISecret   string
	MailjetAPIURL      string
	MailjetRelayKey    string
	MailFromEmail      string
	ContactEmailTo     string
	ContactUpacEmailTo string

	WebpushPublicKey  string
	WebpushPrivateKey string
	WebpushSubject    string

	CORSAllowedOrigins []string
	CORSAllowHeaders   []string
	CORSAllowMethods   []string

	SwaggerEnabled bool
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func LoadConfig() Config {
	port := env("PORT", "8080")
	mailFrom := env("MAIL_FROM_EMAIL", "ni.catalmir@gmail.com")
	contactTo := env("CONTACT_EMAIL_TO", mailFrom)

	origins := []string{
		"http://localhost:3000",
		"http://localhost:8080",
		"http://localhost:4200",
		"http://localhost:8082",
		"https://sphere-481401.web.app",
		"https://sphere-481401.firebaseapp.com",
		"https://api-sphere.onrender.com",
		"https://api.sportspherecenter.com",
		"https://dev.sportspherecenter.com",
	}
	// Orígenes extra sin tocar el código (separados por coma)
	for _, o := range strings.Split(env("CORS_EXTRA_ORIGINS", ""), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}

	return Config{
		Addr: env("LISTEN_ADDR", ":"+port),
		// Mismo valor por defecto que Django para que los JWT ya emitidos sigan siendo válidos
		SecretKey: env("SECRET_KEY", "django-insecure--jc0^^b*j1k=4n38its9)q_5%2m&c)!$-7b$@w_h7=5#bi@&jp"),

		DBHost:    os.Getenv("DB_HOST"),
		DBPort:    env("DB_PORT", "5432"),
		DBName:    os.Getenv("DB_NAME"),
		DBUser:    os.Getenv("DB_USER"),
		DBPass:    os.Getenv("DB_PASS"),
		DBSSLMode: env("DB_SSLMODE", "prefer"),

		JWTCookieSecure:   true,
		JWTCookieSameSite: "None",
		JWTCookieDomain:   "",

		// Login con usuario y contraseña de la app (además del SSO del IAM)
		PasswordLoginEnabled: env("PASSWORD_LOGIN_ENABLED", "True") == "True",

		IAMVerifyLogin:     env("IAM_VERIFY_LOGIN", "True") == "True",
		IAMMeURL:           env("IAM_ME_URL", "https://bff-auth.sportspherecenter.com/auth/me"),
		IAMLocalBypass:     env("IAM_LOCAL_BYPASS", "False") == "True",
		IAMLocalBypassHost: []string{"localhost", "127.0.0.1"},

		// MAILJET_API_URL puede apuntar a un relay si Mailjet bloquea la IP del servidor
		MailjetAPIKey:      os.Getenv("MAILJET_API_KEY"),
		MailjetAPISecret:   os.Getenv("MAILJET_API_SECRET"),
		MailjetAPIURL:      env("MAILJET_API_URL", "https://api.mailjet.com/v3.1/send"),
		MailjetRelayKey:    os.Getenv("MAILJET_RELAY_KEY"),
		MailFromEmail:      mailFrom,
		ContactEmailTo:     contactTo,
		ContactUpacEmailTo: os.Getenv("CONTACT_UPAC_EMAIL_TO"),

		WebpushPublicKey:  env("WEBPUSH_VAPID_PUBLIC_KEY", "BPUz4LbqWQ37yEGwuCBW-pPzYfBwFzgi0H0xT9pvmV2d0BZaBKZi2UhdfXiEno-Q2OTKRPMm9JWjCRzF90hE8M8"),
		WebpushPrivateKey: env("WEBPUSH_VAPID_PRIVATE_KEY", "LOmwxfnxRxMobwrCqIOuf6tZYB6HfA0srmBTN0tQkCk"),
		WebpushSubject:    env("WEBPUSH_VAPID_SUBJECT", "mailto:admin@tu-dominio.com"),

		CORSAllowedOrigins: origins,
		CORSAllowHeaders: []string{
			"accept", "accept-encoding", "authorization", "content-type", "dnt",
			"origin", "user-agent", "x-csrftoken", "x-requested-with",
		},
		CORSAllowMethods: []string{"DELETE", "GET", "OPTIONS", "PATCH", "POST", "PUT"},

		// /swagger/ y /redoc/: activar en dev, dejar apagado en producción
		SwaggerEnabled: env("SWAGGER_ENABLED", "False") == "True",
	}
}
