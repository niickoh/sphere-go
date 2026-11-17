package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFechaES(t *testing.T) {
	cases := map[string]string{
		"2026-11-17T18:30:00": "martes 17 de noviembre de 2026, 18:30",
		"2026-11-17":          "martes 17 de noviembre de 2026",
		"mañana":              "mañana",
	}
	for in, want := range cases {
		if got := fechaES(in); got != want {
			t.Errorf("fechaES(%q) = %q, want %q", in, got, want)
		}
	}
	if fechaES(nil) != "" {
		t.Error("fechaES(nil) debe ser vacío")
	}
}

func TestRecipients(t *testing.T) {
	r, msg := recipients(map[string]any{"email": "a@b.cl"})
	if msg != "" || len(r) != 1 || r[0].name != "a@b.cl" {
		t.Fatalf("email suelto: %v %q", r, msg)
	}
	if _, msg := recipients(map[string]any{"destinatarios": []any{map[string]any{"email": "malo"}}}); msg == "" {
		t.Error("debe rechazar email inválido")
	}
	if _, msg := recipients(map[string]any{"destinatarios": []any{}}); msg == "" {
		t.Error("debe rechazar lista vacía")
	}
}

// Renderiza cada plantilla con datos de ejemplo. Con MAIL_PREVIEW_DIR=<dir> además guarda los HTML.
func TestMailTemplatesRender(t *testing.T) {
	if err := loadTemplates(); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: Config{PortalURL: "https://dev.sportspherecenter.com/"}}
	p := map[string]any{
		"name": "Sofía <b>", "email": "sofia@example.com", "message": "Hola,\nquiero una demo.", "club": "UPAC",
		"titulo": "Cambio de horario", "mensaje": "El jueves entrenamos a las 19:00.", "prioridad": "importante",
		"autor": "Coach Camila", "equipo": "Galaxy", "url": "https://dev.sportspherecenter.com/",
		"fecha_inicio": "2026-11-17T18:30:00", "fecha_fin": "2026-11-17T20:30:00", "ubicacion": "Gimnasio UPAC",
		"descripcion": "Traer botella de agua.", "tipo": "ENTRENAMIENTO",
		"username": "sofia.r", "password": "Temp2026!", "rol": "Atleta", "entrenador": "Camila Rojas",
		"color": "#E91E63",
	}
	specs := map[string]func(map[string]any) map[string]any{
		"aviso.html":             a.avisoSpec().context,
		"evento.html":            a.eventoSpec().context,
		"usuario-creado.html":    a.usuarioCreadoSpec().context,
		"asignacion-equipo.html": a.asignacionEquipoSpec().context,
	}
	specs["contacto.html"] = func(p map[string]any) map[string]any {
		d := a.mailBase()
		for k, v := range map[string]any{"ETIQUETA": "Formulario de contacto", "ASUNTO": "Nuevo mensaje de Sofía",
			"NOMBRE": p["name"], "EMAIL": p["email"], "MENSAJE": p["message"]} {
			d[k] = v
		}
		return d
	}

	dir := os.Getenv("MAIL_PREVIEW_DIR")
	for tpl, ctxFn := range specs {
		data := a.mailBase()
		for k, v := range ctxFn(p) {
			data[k] = v
		}
		if _, ok := data["NOMBRE"]; !ok {
			data["NOMBRE"] = "Sofía <b>"
		}
		out, err := renderTemplate(tpl, data)
		if err != nil {
			t.Fatalf("%s: %v", tpl, err)
		}
		for _, bad := range []string{"None", "<no value>", "Sofía <b>"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s contiene %q", tpl, bad)
			}
		}
		if !strings.Contains(out, "sphere-mail-logo.png") || !strings.HasSuffix(strings.TrimSpace(out), "</html>") {
			t.Errorf("%s: falta logo o cierre del layout", tpl)
		}
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, tpl), []byte(out), 0o644)
		}
	}
}
