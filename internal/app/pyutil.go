package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// OM es un objeto JSON que conserva el orden de las claves, igual que los dict de Python.
type OM []kv

type kv struct {
	K string
	V any
}

// obj("a", 1, "b", 2) -> {"a":1,"b":2}
func obj(pairs ...any) OM {
	m := make(OM, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m = append(m, kv{pairs[i].(string), pairs[i+1]})
	}
	return m
}

func (m OM) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range m {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := marshalJSON(p.K)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		v, err := marshalJSON(p.V)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalJSON codifica sin escapar <, > y & (como el JSONRenderer de DRF).
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ---------- Semántica de Python ----------

// truthy replica bool(x) de Python para valores decodificados de JSON.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		f, err := t.Float64()
		return err != nil || f != 0
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// pyStr replica str(x) de Python (None -> "None", True -> "True").
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		return t
	case json.Number:
		return t.String()
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := marshalJSON(v)
		return string(b)
	}
}

// pyInt replica int(x) de Python: acepta números (trunca decimales), strings con
// espacios y booleanos. ok=false equivale a TypeError/ValueError.
func pyInt(v any) (int64, bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, true
		}
		f, err := t.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return int64(f), true
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(t), "_", "")
		i, err := strconv.ParseInt(s, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// intKey devuelve el entero sólo si el valor JSON es un número entero
// (equivale a usarlo como clave de un dict de ints en Python: "5" no coincide con 5).
func intKey(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	if i, err := n.Int64(); err == nil {
		return i, true
	}
	f, err := n.Float64()
	if err != nil || f != math.Trunc(f) {
		return 0, false
	}
	return int64(f), true
}

// dbText convierte un valor del body a texto para pasarlo a SQL como $n::text::tipo,
// imitando cómo Django convierte el valor al tipo de la columna.
func dbText(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return pyStr(v)
	}
}

// ---------- Fechas ----------

func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func isoBase(t time.Time) string {
	s := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	return s
}

// pyISO replica datetime.isoformat() de una fecha UTC con tz: "...+00:00".
func pyISO(t time.Time) string { return isoBase(t.UTC()) + "+00:00" }

// drfISO replica cómo DRF serializa un datetime dentro de Response: "+00:00" -> "Z".
func drfISO(t time.Time) string { return isoBase(t.UTC()) + "Z" }

// naiveISO replica datetime.now().isoformat() (hora local, sin zona).
func naiveISO(t time.Time) string { return isoBase(t.Local()) }

func pyISOPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return pyISO(*t)
}

func drfISOPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return drfISO(*t)
}

func ptrAny[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
