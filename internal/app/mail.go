package app

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"
)

const (
	mailjetURL     = "https://api.mailjet.com/v3.1/send"
	mailjetTimeout = 15 * time.Second
	maxFieldLength = 5000
)

//go:embed templates/*.html
var templateFS embed.FS

var mailTemplates *template.Template

// pyNone marca un valor None explícito del contexto: Django lo pinta como "None",
// mientras que una variable inexistente se pinta vacía.
type pyNone struct{}

func loadTemplates() error {
	t, err := template.New("").Option("missingkey=zero").Funcs(template.FuncMap{
		// {{ X }} de Django: str() + autoescape HTML
		"v": func(x any) string {
			switch x.(type) {
			case nil:
				return ""
			case pyNone:
				return "None"
			case time.Time:
				return html.EscapeString(djangoDateTime(x.(time.Time)))
			}
			return html.EscapeString(pyStr(x))
		},
		// {% if X %}
		"t": func(x any) bool {
			if _, ok := x.(pyNone); ok {
				return false
			}
			if _, ok := x.(time.Time); ok {
				return true
			}
			return truthy(x)
		},
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("plantillas de correo: %w", err)
	}
	mailTemplates = t
	return nil
}

func renderTemplate(name string, ctx map[string]any) (string, error) {
	data := make(map[string]any, len(ctx))
	for k, v := range ctx {
		if v == nil {
			v = pyNone{}
		}
		data[k] = v
	}
	var buf bytes.Buffer
	if err := mailTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// djangoDateTime replica el filtro por defecto de Django para datetimes (DATETIME_FORMAT 'N j, Y, P').
func djangoDateTime(t time.Time) string {
	months := []string{"Jan.", "Feb.", "March", "April", "May", "June", "July", "Aug.", "Sept.", "Oct.", "Nov.", "Dec."}
	var p string
	switch {
	case t.Hour() == 0 && t.Minute() == 0:
		p = "midnight"
	case t.Hour() == 12 && t.Minute() == 0:
		p = "noon"
	default:
		h := t.Hour() % 12
		if h == 0 {
			h = 12
		}
		f := fmt.Sprint(h)
		if t.Minute() != 0 {
			f = fmt.Sprintf("%d:%02d", h, t.Minute())
		}
		ampm := "a.m."
		if t.Hour() >= 12 {
			ampm = "p.m."
		}
		p = f + " " + ampm
	}
	return fmt.Sprintf("%s %d, %d, %s", months[t.Month()-1], t.Day(), t.Year(), p)
}

// mailjetSend replica mailjet.send.create(data=...) del cliente v3.1.
func (a *App) mailjetSend(ctx context.Context, data any) (int, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, mailjetTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mailjetURL, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(a.cfg.MailjetAPIKey, a.cfg.MailjetAPISecret)
	res, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	log.Printf("mailjet %d: %s", res.StatusCode, bytes.TrimSpace(body))
	return res.StatusCode, nil
}

func (a *App) from() OM {
	return obj("Email", a.cfg.MailFromEmail, "Name", "Sphere Center Pro")
}

func mailStatusResponse(c *Ctx, message string, code int) {
	status := 200
	if code >= 400 {
		status = 502
	}
	c.JSON(status, obj("message", message, "mailStatus", code))
}

// POST apiMail/confirmacionPago/
func (a *App) confirmacionPago(c *Ctx) {
	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}
	email, name := body["email"], body["name"]
	htmlContent, err := renderTemplate("pago-confirmado.html", map[string]any{
		"EMPRESA":       "Sphere Center Pro",
		"NOMBRE":        name,
		"ESTADO_PAGO":   body["estado_pago"],
		"PLAN":          body["plan_suscripcion"],
		"PRODUCTO":      "",
		"MONTO":         body["monto"],
		"FECHA_PAGO":    time.Now(),
		"METODO_PAGO":   body["metodo_pago"],
		"REFERENCIA":    body["idTrx"],
		"URL_PORTAL":    "https://sphere-481401.web.app/",
		"EMAIL_SOPORTE": "contacto@spherepro.com",
	})
	if err != nil {
		c.serverError(err)
		return
	}
	code, err := a.mailjetSend(c.ctx(), obj("Messages", []OM{obj(
		"From", a.from(),
		"To", []OM{obj("Email", email, "Name", name)},
		"Subject", "Confirmación de Pago",
		"TextPart", "Confirmación de Pago",
		"HTMLPart", htmlContent,
	)}))
	if err != nil {
		c.serverError(err)
		return
	}
	mailStatusResponse(c, "Confirmación de pago enviado con éxito", code)
}

// POST apiMail/bienvenidaNew/
func (a *App) bienvenidaNew(c *Ctx) {
	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}
	email, name := body["email"], body["name"]
	htmlContent, err := renderTemplate("ejemplo.html", map[string]any{
		"EMPRESA":    "Sphere Center Pro",
		"NOMBRE":     name,
		"URL_ACCESO": "https://sphere-481401.web.app/",
		"USUARIO":    body["username"],
		"PASSWORD":   body["password"],
	})
	if err != nil {
		c.serverError(err)
		return
	}
	code, err := a.mailjetSend(c.ctx(), obj("Messages", []OM{obj(
		"From", a.from(),
		"To", []OM{obj("Email", email, "Name", name)},
		"Subject", "Bievenida a SportSpherePro",
		"TextPart", "Bievenida a SportSpherePro",
		"HTMLPart", htmlContent,
	)}))
	if err != nil {
		c.serverError(err)
		return
	}
	mailStatusResponse(c, "Correo enviado con éxito", code)
}

// POST apiMail/bienvenida/
func (a *App) bienvenida(c *Ctx) {
	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}
	code, err := a.mailjetSend(c.ctx(), obj("Messages", []OM{obj(
		"From", a.from(),
		"To", []OM{obj("Email", body["email"], "Name", "Nicolas Catalan")},
		"TemplateID", 7708301,
		"TemplateLanguage", true,
		"Subject", "Bievenida a SportSpherePro",
		"Variables", obj("password", body["password"], "username", body["username"], "firstname", body["name"]),
	)}))
	if err != nil {
		c.serverError(err)
		return
	}
	mailStatusResponse(c, "Correo enviado con éxito", code)
}

// ---------- Formularios de contacto ----------

type contactSpec struct {
	required  []string
	template  string
	subject   func(map[string]any) string
	textLines func(map[string]any) []string
	context   func(map[string]any) map[string]any
	recipient string
}

func valueLen(v any) int {
	return utf8.RuneCountInString(pyStr(v))
}

func (a *App) sendContactMessage(c *Ctx, spec contactSpec) {
	payload, ok := c.data()
	if !ok {
		return
	}
	if a.cfg.MailjetAPIKey == "" || a.cfg.MailjetAPISecret == "" || spec.recipient == "" {
		c.JSON(503, obj("error", "El envío de correos no está configurado"))
		return
	}
	var missing []string
	for _, f := range spec.required {
		v := payload[f]
		if !truthy(v) {
			v = ""
		}
		if strings.TrimSpace(pyStr(v)) == "" {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		c.JSON(400, obj("error", strings.Join(missing, ", ")+" son obligatorios"))
		return
	}
	email, _ := payload["email"].(string)
	if !validateEmail(email) {
		c.JSON(400, obj("error", "El email no es válido"))
		return
	}
	for _, v := range payload {
		if valueLen(v) > maxFieldLength {
			c.JSON(400, obj("error", "El mensaje es demasiado largo"))
			return
		}
	}

	fullName := spec.subject(payload)
	htmlContent, err := renderTemplate(spec.template, spec.context(payload))
	if err != nil {
		c.serverError(err)
		return
	}
	code, err := a.mailjetSend(c.ctx(), obj("Messages", []OM{obj(
		"From", a.from(),
		// El mensaje va al administrador; se responde al remitente vía ReplyTo
		"To", []OM{obj("Email", spec.recipient, "Name", "Administrador")},
		"ReplyTo", obj("Email", email, "Name", fullName),
		"Subject", "Nuevo mensaje de contacto de "+fullName,
		"TextPart", strings.Join(spec.textLines(payload), "\n"),
		"HTMLPart", htmlContent,
	)}))
	if err != nil || code >= 400 {
		if err != nil {
			log.Printf("mailjet error: %v", err)
		}
		c.JSON(502, obj("error", "No se pudo enviar el mensaje"))
		return
	}
	c.JSON(200, obj("message", "Mensaje enviado correctamente"))
}

// clubLines: datos opcionales del formulario de la landing (club, teléfono y tamaño del club).
func clubLines(p map[string]any) []string {
	var out []string
	for _, f := range [][2]string{{"Club", "club"}, {"Teléfono", "telefono"}, {"N° de deportistas", "deportistas"}} {
		if truthy(p[f[1]]) {
			out = append(out, f[0]+": "+pyStr(p[f[1]]))
		}
	}
	return out
}

// POST apiMail/contacto/
func (a *App) contacto(c *Ctx) {
	a.sendContactMessage(c, contactSpec{
		required: []string{"name", "email", "message"},
		template: "contacto.html",
		subject:  func(p map[string]any) string { return pyStr(p["name"]) },
		textLines: func(p map[string]any) []string {
			lines := []string{"Nombre: " + pyStr(p["name"]), "Correo: " + pyStr(p["email"])}
			lines = append(lines, clubLines(p)...)
			return append(lines, "", "Mensaje:", pyStr(p["message"]))
		},
		context: func(p map[string]any) map[string]any {
			msg := p["message"]
			if !truthy(msg) {
				msg = ""
			}
			parts := append(clubLines(p), "", pyStr(msg))
			return map[string]any{
				"EMPRESA": "Sphere Center Pro",
				"NOMBRE":  p["name"],
				"EMAIL":   p["email"],
				"MENSAJE": strings.TrimSpace(strings.Join(parts, "\n")),
			}
		},
		recipient: a.cfg.ContactEmailTo,
	})
}

// POST apiMail/contacto-upac/
func (a *App) contactoUpac(c *Ctx) {
	recipient := a.cfg.ContactUpacEmailTo
	if recipient == "" {
		recipient = a.cfg.ContactEmailTo
	}
	a.sendContactMessage(c, contactSpec{
		required: []string{"nombre", "apellidos", "edad", "email", "telefono", "direccion", "mensaje"},
		template: "contacto_upac.html",
		subject: func(p map[string]any) string {
			return strings.TrimSpace(pyStr(p["nombre"]) + " " + pyStr(p["apellidos"]))
		},
		textLines: func(p map[string]any) []string {
			return []string{
				"Nombre: " + pyStr(p["nombre"]),
				"Apellidos: " + pyStr(p["apellidos"]),
				"Edad: " + pyStr(p["edad"]),
				"Correo: " + pyStr(p["email"]),
				"Teléfono: " + pyStr(p["telefono"]),
				"Dirección: " + pyStr(p["direccion"]),
				"",
				"Mensaje:",
				pyStr(p["mensaje"]),
			}
		},
		context: func(p map[string]any) map[string]any {
			return map[string]any{
				"EMPRESA":   "UPAC All Stars",
				"NOMBRE":    p["nombre"],
				"APELLIDOS": p["apellidos"],
				"EDAD":      p["edad"],
				"EMAIL":     p["email"],
				"TELEFONO":  p["telefono"],
				"DIRECCION": p["direccion"],
				"MENSAJE":   p["mensaje"],
			}
		},
		recipient: recipient,
	})
}

// ---------- django.core.validators.validate_email ----------

var (
	emailUserRe    = regexp.MustCompile(`(?i)(^[-!#$%&'*+/=?^_` + "`" + `{}|~0-9A-Z]+(\.[-!#$%&'*+/=?^_` + "`" + `{}|~0-9A-Z]+)*$|^"([\x01-\x08\x0b\x0c\x0e-\x1f!#-\[\]-\x7f]|\\[\x01-\x09\x0b\x0c\x0e-\x7f])*"$)`)
	emailDomainRe  = regexp.MustCompile(`(?i)^((?:[A-Z0-9](?:[A-Z0-9-]{0,61}[A-Z0-9])?\.)+)([A-Z0-9-]{2,63})$`)
	emailLiteralRe = regexp.MustCompile(`^\[([A-F0-9:.]+)\]$`)
)

func validateEmail(value string) bool {
	if value == "" || !strings.Contains(value, "@") {
		return false
	}
	i := strings.LastIndex(value, "@")
	user, domain := value[:i], value[i+1:]
	if !emailUserRe.MatchString(user) {
		return false
	}
	if domain == "localhost" {
		return true
	}
	if m := emailDomainRe.FindStringSubmatch(domain); m != nil {
		return !strings.HasSuffix(m[2], "-")
	}
	return emailLiteralRe.MatchString(strings.ToUpper(domain))
}
