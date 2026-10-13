package app

import (
	_ "embed"
	"net/http"
)

// Documentación de la API (reemplaza a drf_yasg). Solo se publica con SWAGGER_ENABLED=True.

//go:embed docs/openapi.yaml
var openAPISpec []byte

const swaggerUIVersion = "5.17.14"
const redocVersion = "2.1.5"

const swaggerHTML = `<!doctype html>
<html lang="es">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>API Sphere - Swagger</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: "../swagger.yaml",
      dom_id: "#swagger-ui",
      deepLinking: true,
      persistAuthorization: true,
      displayRequestDuration: true,
      tryItOutEnabled: true
    });
  </script>
</body>
</html>
`

const redocHTML = `<!doctype html>
<html lang="es">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>API Sphere - ReDoc</title>
  <style>body { margin: 0; }</style>
</head>
<body>
  <redoc spec-url="../swagger.yaml"></redoc>
  <script src="https://cdn.jsdelivr.net/npm/redoc@` + redocVersion + `/bundles/redoc.standalone.js"></script>
</body>
</html>
`

// serveDocs atiende /swagger/, /redoc/ y /swagger.yaml. Devuelve false si la ruta no es de documentación.
func (a *App) serveDocs(w http.ResponseWriter, r *http.Request, path string) bool {
	if !a.cfg.SwaggerEnabled {
		return false
	}
	var body []byte
	var contentType string
	switch path {
	case "swagger", "redoc":
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return true
	case "swagger/":
		body, contentType = []byte(swaggerHTML), "text/html; charset=utf-8"
	case "redoc/":
		body, contentType = []byte(redocHTML), "text/html; charset=utf-8"
	case "swagger.yaml":
		body, contentType = openAPISpec, "application/yaml; charset=utf-8"
	default:
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
	return true
}
