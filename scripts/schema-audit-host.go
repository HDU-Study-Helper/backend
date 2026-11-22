//go:build ignore

// Offline host-side schema inventory. Does not connect databases or providers.
package main
import("encoding/json";"fmt";"os"; f "academic/agent/framework"; "academic/backend/internal/agent")
func main(){r:=f.NewToolRegistry();if e:=agent.RegisterScheduleTools(r,nil);e!=nil{panic(e)};d:=map[string]any{"source":"RegisterScheduleTools actual registration; nil backend is never invoked","tools":r.Catalog()};raw,_:=json.MarshalIndent(d,"","  ");if e:=os.WriteFile("../.build/schema-audit-20261117/schedule-catalog.json",raw,0600);e!=nil{panic(e)};fmt.Println("Registered and compiled",len(r.Catalog()),"schedule input/output schemas without executing handlers")}
