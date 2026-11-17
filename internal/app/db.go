package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openDB(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.DBUser, cfg.DBPass),
		Host:   net.JoinHostPort(cfg.DBHost, cfg.DBPort),
		Path:   "/" + cfg.DBName,
	}
	q := u.Query()
	q.Set("sslmode", cfg.DBSSLMode)
	// Igual que 'connect_timeout': 10 de settings.py
	q.Set("connect_timeout", "10")
	u.RawQuery = q.Encode()

	pc, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("configuración de base de datos inválida: %w", err)
	}
	// Django con USE_TZ=True trabaja la conexión en UTC
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pc.MaxConns = 10
	pc.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("no se pudo conectar a PostgreSQL: %w", err)
	}
	return pool, nil
}

// schemaInfo guarda diferencias posibles del esquema real respecto a los modelos
// (p. ej. date_joined existe en el modelo pero ninguna migración lo crea).
type schemaInfo struct {
	tables          map[string]bool
	userDateJoined  bool
	userColumnsSeen map[string]bool
}

func loadSchemaInfo(ctx context.Context, db *pgxpool.Pool) (*schemaInfo, error) {
	s := &schemaInfo{tables: map[string]bool{}, userColumnsSeen: map[string]bool{}}
	rows, err := db.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		s.tables[n] = true
	}
	if !s.tables["usuarios_usuarios"] {
		return nil, errors.New("la base de datos no tiene la tabla usuarios_usuarios (¿DB_NAME correcto?)")
	}

	// Cambios de esquema de esta versión. No hay migraciones: cada sentencia es aditiva e idempotente
	// (se puede correr en cada arranque) y no modifica datos existentes.
	for _, q := range []string{
		// Planes: tipo (mensualidad, clase o pack), disciplina y cantidad de clases de los packs.
		// Los planes existentes quedan como mensualidad, que es lo que eran.
		`ALTER TABLE finanzas_plansuscripciones ADD COLUMN IF NOT EXISTS tipo varchar(20) NOT NULL DEFAULT 'mensualidad'`,
		`ALTER TABLE finanzas_plansuscripciones ADD COLUMN IF NOT EXISTS disciplina varchar(50)`,
		`ALTER TABLE finanzas_plansuscripciones ADD COLUMN IF NOT EXISTS "cantidadClases" integer`,
		// Perfiles adicionales del usuario (p. ej. un coach que también es atleta). Los permisos los da
		// siempre tipo_id (el perfil principal); estos solo suman las funciones de esos roles.
		`ALTER TABLE usuarios_usuarios ADD COLUMN IF NOT EXISTS perfiles integer[] NOT NULL DEFAULT '{}'`,
		// A quién se dirigió cada aviso ("Todo el club", "Equipos: ..."), para mostrarlo en el tablón
		`ALTER TABLE avisos_aviso ADD COLUMN IF NOT EXISTS audiencia varchar(200)`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			return nil, fmt.Errorf("actualizando el esquema: %w", err)
		}
	}
	// Rol Staff: ve lo mismo que un entrenador, sin crear ni editar. Si no se puede crear no se
	// impide el arranque; solo no se podrán asignar usuarios Staff.
	if _, err := db.Exec(ctx, `INSERT INTO usuarios_tipousuario (id, description)
		SELECT $1, 'Staff' WHERE NOT EXISTS (SELECT 1 FROM usuarios_tipousuario WHERE id = $1)`, tipoStaff); err != nil {
		log.Printf("aviso: no se pudo crear el tipo de usuario Staff (id %d): %v", tipoStaff, err)
	}
	rows, err = db.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'usuarios_usuarios'`)
	if err != nil {
		return nil, err
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		s.userColumnsSeen[c] = true
	}
	s.userDateJoined = s.userColumnsSeen["date_joined"]
	return s, nil
}

// fkRule describe un on_delete de Django que hay que emular (en la BD las FK no tienen ON DELETE).
type fkRule struct {
	table, column string
	cascade       bool // CASCADE; si no, SET_NULL
}

var deleteRules = map[string][]fkRule{
	"usuarios_usuarios": {
		{"usuarios_usersteams", "idUser_id", false},
		{"usuarios_datosmedicos", "idUser_id", false},
		{"finanzas_suscripciones", "userId_id", false},
		{"avisos_aviso", "creado_por_id", false},
		{"avisos_avisodestinatario", "idUser_id", false},
		{"avisos_pushsuscriptions", "idUser_id", false},
		{"token_blacklist_outstandingtoken", "user_id", false},
		{"authtoken_token", "user_id", true},
		{"django_admin_log", "user_id", true},
		{"usuarios_usuarios_groups", "usuarios_id", true},
		{"usuarios_usuarios_user_permissions", "usuarios_id", true},
	},
	"usuarios_equipos": {
		{"usuarios_usersteams", "idTeam_id", false},
		{"eventos_evento", "equipo_id", true},
	},
	"finanzas_plansuscripciones": {
		{"finanzas_suscripciones", "planId_id", false},
	},
	"avisos_aviso": {
		{"avisos_avisodestinatario", "idAviso_id", false},
	},
}

// deleteByID borra una fila emulando el Collector de Django. Devuelve false si no existía.
func (a *App) deleteByID(ctx context.Context, table string, id int64) (bool, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)`, table), id).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	for _, r := range deleteRules[table] {
		if !a.schema.tables[r.table] {
			continue
		}
		var q string
		if r.cascade {
			q = fmt.Sprintf(`DELETE FROM %s WHERE %q = $1`, r.table, r.column)
		} else {
			q = fmt.Sprintf(`UPDATE %s SET %q = NULL WHERE %q = $1`, r.table, r.column, r.column)
		}
		if _, err := tx.Exec(ctx, q, id); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, table), id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// pgErrText arma un texto similar a str(IntegrityError) de psycopg2 (mensaje + DETAIL).
func pgErrText(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		s := pe.Message
		if pe.Detail != "" {
			s += "\nDETAIL:  " + pe.Detail
		}
		return s + "\n"
	}
	return err.Error()
}

func isIntegrityErr(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && strings.HasPrefix(pe.Code, "23")
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
