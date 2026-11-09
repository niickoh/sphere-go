# API Sphere en Go

Port 1:1 del backend Django de `../sphere`. Usa **la misma base PostgreSQL** (no requiere
migraciones), las mismas rutas, los mismos JSON de respuesta, las mismas cookies
(`access_token` / `refresh_token`) y JWT compatibles con los ya emitidos por Django.
El front no necesita cambios.

## Compilar

```bash
# Binario para el VPS (Linux) desde cualquier sistema
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o sphere-api .

# O con Docker
docker build -t sphere-api .
```

Requiere Go 1.25 o superior.

## Configuración

Variables de entorno (o archivo `.env` en el directorio de trabajo). Ver `.env.example`.
Son las mismas que usaba Django. Lo importante:

- `SECRET_KEY`: **el mismo valor que en Django**. Así los usuarios con sesión abierta no la pierden al migrar.
- `DB_*`: la base actual. `DB_SSLMODE` es nueva (por defecto `prefer`).
- `PORT`: puerto local (por defecto 8080). Nginx expone HTTPS hacia afuera.

## Instalar en el VPS (systemd + nginx)

```bash
sudo useradd --system --no-create-home sphere
sudo mkdir -p /opt/sphere-api
sudo cp sphere-api /opt/sphere-api/ && sudo cp .env /opt/sphere-api/.env
sudo chown -R sphere:sphere /opt/sphere-api && sudo chmod 600 /opt/sphere-api/.env

sudo cp deploy/sphere-api.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now sphere-api
journalctl -u sphere-api -f        # logs

sudo cp deploy/nginx.conf /etc/nginx/sites-available/api.sportspherecenter.com
sudo ln -s /etc/nginx/sites-available/api.sportspherecenter.com /etc/nginx/sites-enabled/
sudo certbot --nginx -d api.sportspherecenter.com && sudo systemctl reload nginx
```

Para actualizar: copiar el binario nuevo y `sudo systemctl restart sphere-api`.

El login valida la sesión del IAM leyendo la cookie del BFF, que vive en `.sportspherecenter.com`.
Por eso la API tiene que seguir publicada bajo ese dominio (p. ej. `api.sportspherecenter.com`).

## Swagger (documentación y pruebas)

Con `SWAGGER_ENABLED=True` en el `.env` se publican:

- `/swagger/`: Swagger UI, para probar los endpoints desde el navegador.
- `/redoc/`: la misma documentación en formato de lectura.
- `/swagger.yaml`: la especificación OpenAPI (`internal/app/docs/openapi.yaml`, va incluida en el binario).

Viene apagado por defecto: actívalo en dev y déjalo apagado en producción. Si cambias un
endpoint, actualiza también `openapi.yaml`.

Para probar endpoints protegidos:

1. En Swagger ejecuta `POST /auth/login/` con tu `username` y `password`. Las cookies de sesión
   quedan guardadas y Swagger las envía solas en las siguientes peticiones.
2. Si pasan 15 minutos y empiezas a recibir 401, ejecuta `POST /auth/refresh/`.

El login tiene dos modos:

- **Contraseña** (`username` + `password`): el login propio de la app. Se apaga con
  `PASSWORD_LOGIN_ENABLED=False` (responde 403).
- **SSO** (solo `username`): exige una sesión del IAM del mismo usuario (cookie del BFF). Para
  probarlo desde Swagger, inicia sesión antes en el front de dev con el botón de SSO.

Con la API en `localhost` puedes saltarte el IAM con `IAM_LOCAL_BYPASS=True`. No pongas
`IAM_VERIFY_LOGIN=False` en un servidor público: el login por SSO no pide contraseña, así que
cualquiera podría entrar como cualquier usuario.

## Qué se portó

Todos los endpoints de `mysite/urls.py`: auth, usuarios, datos médicos, equipos, eventos,
finanzas (planes, suscripciones y pagos), Transbank Webpay (integración), reportes,
avisos con Web Push y correos con Mailjet (plantillas en `internal/app/templates`).

Se replicó también el comportamiento de Django/DRF: autenticación por cookie o `Bearer`,
mensajes de error 401/403/405 de DRF, CORS, redirección de barra final (`APPEND_SLASH`),
formato de fechas, IDs `MAX(id)+1` de finanzas y los `on_delete` (SET NULL / CASCADE) al borrar.

**No se portó:** el admin de Django (`/admin/`) ni la app `notifications`, que no tenía rutas
activas. Swagger y ReDoc se reemplazaron por una especificación escrita a mano (ver arriba).

## Diferencias intencionales con Django

- **Borrar usuario** (`DELETE usuarios/eliminar/<id>`): en Django siempre devolvía 500, porque
  intentaba limpiar la tabla `usuarios_usuarios_groups`, que ninguna migración crea. En Go el
  usuario se borra y sus referencias quedan en NULL, como define el modelo.
- **Logout con refresh token inválido o vencido**: Django respondía 500. Ahora borra las cookies igual.
- **`avisos/mis-avisos`**: Django fallaba si el usuario tenía destinatarios de avisos ya borrados.
  Ahora esos registros se omiten.
- Los mensajes de `JSON parse error - ...` vienen del parser de Go y no del de Python.

## Comportamientos heredados que se mantuvieron (posibles mejoras)

- "Recuérdame" no alarga el access token (dura 15 min) ni el refresh token (dura 1 día). Solo
  cambia el `max-age` de las cookies. En Django el token se generaba antes de aplicar la duración larga.
- Las credenciales de Transbank son las del ambiente de **integración** (pruebas).
- `asignarPlanesUser` permite varias suscripciones por usuario, pero `getSuscription` responde
  500 si el usuario tiene más de una (igual que Django).

## Tests

```bash
go test ./...
```
