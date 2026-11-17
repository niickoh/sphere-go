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
		"fecha": fechaES,
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.MailjetAPIURL, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(a.cfg.MailjetAPIKey, a.cfg.MailjetAPISecret)
	// Clave compartida con el relay (Cloudflare Worker) cuando no se llama directo a Mailjet
	if a.cfg.MailjetRelayKey != "" {
		req.Header.Set("X-Relay-Key", a.cfg.MailjetRelayKey)
	}
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

// POST apiMail/bienvenidaNew/ — aviso de creación de usuario (plantilla usuario-creado.html)
func (a *App) bienvenidaNew(c *Ctx) { a.sendNotification(c, a.usuarioCreadoSpec()) }

func (a *App) usuarioCreadoSpec() notifSpec {
	return notifSpec{
		required: []string{"username"},
		template: "usuario-creado.html",
		context: func(p map[string]any) map[string]any {
			return map[string]any{
				"ETIQUETA":  "Cuenta creada",
				"ASUNTO":    "Bienvenido/a a SportSphere Pro",
				"PREHEADER": "Tus datos de acceso a SportSphere Pro",
				"USUARIO":   opt(p, "username"),
				"PASSWORD":  opt(p, "password"),
				"ROL":       opt(p, "rol"),
				"CLUB":      opt(p, "club"),
				"BOTON":     "Iniciar sesión",
				"URL":       optOr(p, "url", a.cfg.PortalURL),
				"NOTA":      "Si no esperabas este correo, puedes ignorarlo.",
			}
		},
	}
}

// POST apiMail/aviso/
func (a *App) avisoMail(c *Ctx) { a.sendNotification(c, a.avisoSpec()) }

func (a *App) avisoSpec() notifSpec {
	return notifSpec{
		required: []string{"titulo", "mensaje"},
		template: "aviso.html",
		context: func(p map[string]any) map[string]any {
			prioridad := strings.ToLower(optOr(p, "prioridad", "normal"))
			color, ok := map[string]string{"normal": "#0075BE", "importante": "#f59e0b", "urgente": "#dc2626"}[prioridad]
			if !ok {
				prioridad, color = "normal", "#0075BE"
			}
			asunto := opt(p, "titulo")
			if prioridad != "normal" {
				asunto = "[" + strings.ToUpper(prioridad) + "] " + asunto
			}
			return map[string]any{
				"ETIQUETA":        "Nuevo aviso",
				"ASUNTO":          asunto,
				"PREHEADER":       opt(p, "mensaje"),
				"TITULO":          opt(p, "titulo"),
				"MENSAJE":         opt(p, "mensaje"),
				"PRIORIDAD":       prioridad,
				"PRIORIDAD_COLOR": color,
				"AUTOR":           opt(p, "autor"),
				"EQUIPO":          opt(p, "equipo"),
				"BOTON":           "Ver aviso",
				"URL":             opt(p, "url"),
			}
		},
	}
}

// POST apiMail/evento/ — avisos de calendario (entrenamientos, competencias, reuniones...)
func (a *App) eventoMail(c *Ctx) { a.sendNotification(c, a.eventoSpec()) }

func (a *App) eventoSpec() notifSpec {
	return notifSpec{
		required: []string{"titulo", "fecha_inicio"},
		template: "evento.html",
		context: func(p map[string]any) map[string]any {
			tipo := optOr(p, "tipo", "ENTRENAMIENTO")
			estado := strings.ToLower(optOr(p, "estado", "nuevo"))
			var etiqueta, intro, color string
			switch estado {
			case "modificado":
				etiqueta, intro, color = "Evento modificado", "Se actualizó un evento de tu calendario:", "#f59e0b"
			case "cancelado":
				etiqueta, intro, color = "Evento cancelado", "Se canceló el siguiente evento:", "#dc2626"
			default:
				estado, etiqueta, intro, color = "nuevo", "Nuevo evento en tu calendario", "Se agendó un nuevo evento en tu calendario:", "#0075BE"
			}
			titulo := opt(p, "titulo")
			return map[string]any{
				"ETIQUETA":     etiqueta,
				"ASUNTO":       titulo + " · " + fechaES(p["fecha_inicio"]),
				"PREHEADER":    etiqueta + ": " + titulo,
				"INTRO":        intro,
				"TITULO":       titulo,
				"TIPO":         tipo,
				"ESTADO_COLOR": color,
				"CANCELADO":    estado == "cancelado",
				"FECHA_INICIO": opt(p, "fecha_inicio"),
				"FECHA_FIN":    opt(p, "fecha_fin"),
				"UBICACION":    opt(p, "ubicacion"),
				"EQUIPO":       opt(p, "equipo"),
				"DESCRIPCION":  opt(p, "descripcion"),
				"BOTON":        "Ver calendario",
				"URL":          opt(p, "url"),
			}
		},
	}
}

// POST apiMail/asignacionEquipo/
func (a *App) asignacionEquipoMail(c *Ctx) { a.sendNotification(c, a.asignacionEquipoSpec()) }

func (a *App) asignacionEquipoSpec() notifSpec {
	return notifSpec{
		required: []string{"equipo"},
		template: "asignacion-equipo.html",
		context: func(p map[string]any) map[string]any {
			color := opt(p, "color")
			if !hexColorRe.MatchString(color) { // va dentro de style="": solo colores hex
				color = "#359FD8"
			}
			return map[string]any{
				"ETIQUETA":   "Asignación de equipo",
				"ASUNTO":     "Ahora eres parte de " + opt(p, "equipo"),
				"PREHEADER":  "Fuiste asignado/a al equipo " + opt(p, "equipo"),
				"EQUIPO":     opt(p, "equipo"),
				"ROL":        opt(p, "rol"),
				"ENTRENADOR": opt(p, "entrenador"),
				"CLUB":       opt(p, "club"),
				"COLOR":      color,
				"BOTON":      "Ver mi equipo",
				"URL":        optOr(p, "url", a.cfg.PortalURL),
			}
		},
	}
}

// ---------- Notificaciones a usuarios (aviso, evento, usuario creado, asignación de equipo) ----------

const mailjetBatch = 50 // máximo de Messages por llamada a Mailjet v3.1

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

type notifSpec struct {
	required []string
	template string
	context  func(map[string]any) map[string]any
}

// opt devuelve el campo como texto, o "" si no viene (evita que nil se pinte "None").
func opt(p map[string]any, k string) string {
	if !truthy(p[k]) {
		return ""
	}
	return strings.TrimSpace(pyStr(p[k]))
}

func optOr(p map[string]any, k, def string) string {
	if v := opt(p, k); v != "" {
		return v
	}
	return def
}

func missingFields(p map[string]any, fields []string) []string {
	var missing []string
	for _, f := range fields {
		if opt(p, f) == "" {
			missing = append(missing, f)
		}
	}
	return missing
}

func (a *App) mailBase() map[string]any {
	return map[string]any{
		"EMPRESA":    "SportSphere Pro",
		"URL_PORTAL": a.cfg.PortalURL,
		"LOGO":       strings.TrimRight(a.cfg.PortalURL, "/") + "/assets/images/sphere-mail-logo.png",
	}
}

type recipient struct{ email, name string }

// recipients lee "destinatarios": [{email, name}] o, si no viene, email/name sueltos.
func recipients(p map[string]any) ([]recipient, string) {
	list, isList := p["destinatarios"].([]any)
	if !isList {
		list = []any{map[string]any{"email": p["email"], "name": p["name"]}}
	}
	if len(list) == 0 {
		return nil, "destinatarios no puede estar vacío"
	}
	out := make([]recipient, 0, len(list))
	for _, item := range list {
		m, _ := item.(map[string]any)
		email := opt(m, "email")
		if !validateEmail(email) {
			return nil, "Email no válido: " + email
		}
		out = append(out, recipient{email, optOr(m, "name", email)})
	}
	return out, ""
}

func (a *App) sendNotification(c *Ctx, spec notifSpec) {
	p, ok := c.data()
	if !ok {
		return
	}
	if a.cfg.MailjetAPIKey == "" || a.cfg.MailjetAPISecret == "" {
		c.JSON(503, obj("error", "El envío de correos no está configurado"))
		return
	}
	if missing := missingFields(p, spec.required); len(missing) > 0 {
		c.JSON(400, obj("error", strings.Join(missing, ", ")+" son obligatorios"))
		return
	}
	for _, v := range p {
		if s, isStr := v.(string); isStr && utf8.RuneCountInString(s) > maxFieldLength {
			c.JSON(400, obj("error", "El mensaje es demasiado largo"))
			return
		}
	}
	to, errMsg := recipients(p)
	if errMsg != "" {
		c.JSON(400, obj("error", errMsg))
		return
	}

	data := a.mailBase()
	for k, v := range spec.context(p) {
		data[k] = v
	}
	msgs := make([]OM, 0, len(to))
	for _, r := range to {
		data["NOMBRE"] = r.name
		htmlContent, err := renderTemplate(spec.template, data)
		if err != nil {
			c.serverError(err)
			return
		}
		msgs = append(msgs, obj(
			"From", a.from(),
			"To", []OM{obj("Email", r.email, "Name", r.name)},
			"Subject", data["ASUNTO"],
			"TextPart", pyStr(data["ASUNTO"])+"\n\n"+pyStr(data["PREHEADER"])+"\n\n"+a.cfg.PortalURL,
			"HTMLPart", htmlContent,
		))
	}
	code := 0
	for i := 0; i < len(msgs); i += mailjetBatch {
		var err error
		code, err = a.mailjetSend(c.ctx(), obj("Messages", msgs[i:min(i+mailjetBatch, len(msgs))]))
		if err != nil || code >= 400 {
			if err != nil {
				log.Printf("mailjet error: %v", err)
			}
			c.JSON(502, obj("error", "No se pudo enviar el correo", "enviados", i, "mailStatus", code))
			return
		}
	}
	c.JSON(200, obj("message", "Correo enviado con éxito", "enviados", len(msgs), "mailStatus", code))
}

var (
	diasES  = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	mesesES = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
)

// fechaES formatea una fecha ISO como "lunes 17 de noviembre de 2026, 18:30" (hora de Chile si trae zona).
// Si no se puede leer, la devuelve tal cual.
func fechaES(v any) string {
	s := strings.TrimSpace(pyStr(v))
	if !truthy(v) {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		if layout == time.RFC3339 {
			if loc, err := time.LoadLocation("America/Santiago"); err == nil {
				t = t.In(loc)
			}
		}
		out := fmt.Sprintf("%s %d de %s de %d", diasES[t.Weekday()], t.Day(), mesesES[t.Month()-1], t.Year())
		if layout != "2006-01-02" {
			out += t.Format(", 15:04")
		}
		return out
	}
	return s
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
			data := a.mailBase()
			for k, v := range map[string]any{
				"ETIQUETA":  "Formulario de contacto",
				"ASUNTO":    "Nuevo mensaje de " + pyStr(p["name"]),
				"PREHEADER": pyStr(msg),
				"NOMBRE":    p["name"],
				"EMAIL":     p["email"],
				"MENSAJE":   strings.TrimSpace(strings.Join(parts, "\n")),
			} {
				data[k] = v
			}
			return data
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
