package rooms

import (
	"strings"
	"testing"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

func TestIDsAndStartTransitions(t *testing.T) {
	if id:=BranchID("Feature/Hello_World","fabric",false);id!="feature-hello-world-fabric"{t.Fatal(id)}
	long:=BranchID(strings.Repeat("branch",20),"fabric",false)
	if len(long)>40||!strings.HasSuffix(long,"-fabric"){t.Fatal(long)}
	if BranchID("a/b","fabric",true)==BranchID("a_b","fabric",true){t.Fatal("collision suffix is not tied to original branch")}
	if !strings.HasPrefix(BranchID("中文","fabric",false),"branch-"){t.Fatal("empty slug was not named")}
	for _,state:=range []string{"stopped","closed","failed","queued","building","starting","ready"}{if err:=CanStart(model.Room{State:state,Label:"testing"});err!=nil{t.Fatal(state,err)}}
	for _,state:=range []string{"stopping","archiving","lost"}{if CanStart(model.Room{State:state,Label:"testing"})==nil{t.Fatal(state)}}
	if CanStart(model.Room{State:"ready",Label:"finished"})==nil{t.Fatal("finished room can start")}
}
