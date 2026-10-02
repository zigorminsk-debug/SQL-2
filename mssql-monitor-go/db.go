// db.go — подключение к MS SQL Server через go-mssqldb (чистый Go, без CGO).
// Windows-аутентификация: на Windows — SSPI (текущий пользователь или ДОМЕН\пользователь),
// на Linux/macOS — NTLM (требуется логин в формате ДОМЕН\пользователь).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

type Database struct {
	Server       string
	Database_    string
	WindowsAuth  bool
	User         string
	Password     string
	Port         int
	Encrypt      bool
	TrustCert    bool
	LoginTimeout int

	pool       *sql.DB
	driverName string
}

func NewDatabase(server, database string, windowsAuth bool, user, password string,
	port int, encrypt, trustCert bool) *Database {
	if database == "" {
		database = "master"
	}
	if port == 0 {
		port = 1433
	}
	return &Database{Server: strings.TrimSpace(server), Database_: database,
		WindowsAuth: windowsAuth, User: strings.TrimSpace(user), Password: password,
		Port: port, Encrypt: encrypt, TrustCert: trustCert, LoginTimeout: 15}
}

func (d *Database) dsn() string {
	server := d.Server
	parts := []string{"server=" + server, "database=" + d.Database_}
	hasInstance := strings.Contains(server, `\`)
	if !hasInstance && d.Port != 0 && d.Port != 1433 {
		parts = append(parts, fmt.Sprintf("port=%d", d.Port))
	}
	if !d.WindowsAuth {
		parts = append(parts, "user id="+d.User, "password="+d.Password)
	} else if d.User != "" {
		// SSPI/NTLM с явными учётными данными: ДОМЕН\пользователь
		parts = append(parts, "user id="+d.User)
		if d.Password != "" {
			parts = append(parts, "password="+d.Password)
		}
	}
	if d.Encrypt {
		parts = append(parts, "encrypt=true")
		if d.TrustCert {
			parts = append(parts, "TrustServerCertificate=true")
		}
	} else {
		parts = append(parts, "encrypt=disable")
	}
	parts = append(parts, fmt.Sprintf("connection timeout=%d", d.LoginTimeout))
	parts = append(parts, "app name=MS SQL Monitor")
	return strings.Join(parts, ";")
}

func (d *Database) Connect() error {
	pool, err := sql.Open("sqlserver", d.dsn())
	if err != nil {
		return fmt.Errorf("не удалось подготовить подключение: %w", err)
	}
	pool.SetMaxOpenConns(2)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(d.LoginTimeout+5)*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("подключение к %s не удалось: %w\nПроверьте имя сервера/порт, "+
			"что TCP/IP включён (SQL Server Configuration Manager), учётные данные и "+
			"брандмауэр: %v", d.Server, err, err)
	}
	d.pool = pool
	d.driverName = "go-mssqldb (TDS)"
	return nil
}

func (d *Database) DriverName() string { return d.driverName }
func (d *Database) ServerName() string { return d.Server }
func (d *Database) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}

// Query выполняет SELECT и возвращает строки как map[колонка]значение.
func (d *Database) Query(q string) ([]map[string]any, error) {
	return d.QueryTimeout(q, 90*time.Second)
}

func (d *Database) QueryTimeout(q string, timeout time.Duration) ([]map[string]any, error) {
	if d.pool == nil {
		return nil, &simpleErr{"нет активного подключения"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rows, err := d.pool.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for rows.Next() {
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[strings.ToLower(c)] = v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Test возвращает информацию о версии сервера.
func (d *Database) Test() (map[string]any, error) {
	row := queryRow(d, "SELECT @@SERVERNAME AS srv, @@VERSION AS v")
	if row == nil {
		return nil, &simpleErr{"сервер не вернул версию"}
	}
	return row, nil
}
