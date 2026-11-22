//go:build ignore

package main
import("context";"fmt";"os";"strings";"time";"net/url";"github.com/jackc/pgx/v5")
func main(){ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel();raw:=os.Getenv("DATABASE_URL");c,e:=pgx.Connect(ctx,raw);if e!=nil{panic("database unavailable")};defer c.Close(ctx);const name="academic_closure_test";var exists bool;if c.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`,name).Scan(&exists)!=nil{panic("cannot inspect test DB")};if !exists{if _,e=c.Exec(ctx,"CREATE DATABASE "+pgx.Identifier{name}.Sanitize());e!=nil{panic("cannot create dedicated test database")}}
if strings.HasPrefix(raw,"postgres://")||strings.HasPrefix(raw,"postgresql://"){u,e:=url.Parse(raw);if e!=nil{panic("invalid DSN")};u.Path="/"+name;raw=u.String()}else{raw+=" dbname="+name}
if os.WriteFile("../.build/closure-test-database.dsn",[]byte(raw),0600)!=nil{panic("cannot persist private test DSN")};fmt.Println("Dedicated test database ready:",name)}
