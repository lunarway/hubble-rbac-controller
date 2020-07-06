package redshift

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"os"
	"testing"
)

func init() {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)
	//log.SetFormatter(&log.JSONFormatter{PrettyPrint:true})
}


func buildCurrent() Model {

	model := Model{}
	model.DeclareCluster("dev")

	return model
}

func buildDesired() Model {

	model := Model{}
	cluster := model.DeclareCluster("dev")
	biGroup := cluster.DeclareGroup("bianalyst")
	database := cluster.DeclareDatabase("jwr")
	biDatabaseGroup := database.DeclareGroup("bianalyst")
	cluster.DeclareUser("jwr_bianalyst", biGroup)
	database.DeclareUser("jwr_bianalyst")
	cluster.DeclareUser("jwr_bianalyst2", biGroup)
	biDatabaseGroup.GrantSchema(&Schema{ Name: "public" })

	return model
}

func Test_Dag1(t *testing.T) {

	//assert := assert.New(t)

	//logger := zap.Logger()

	current := buildCurrent()
	desired := buildDesired()
	dagBuilder := NewDagBuilder()
	dagBuilder.UpdateModel(current, desired)

	//b, _ := json.MarshalIndent(dagBuilder, "", "   ")
	//log.Info(fmt.Sprintf("%s", b))
	fmt.Println(dagBuilder.String())
}
