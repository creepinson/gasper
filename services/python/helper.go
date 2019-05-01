package python

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	validator "github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"
	"github.com/sdslabs/SWS/lib/api"
	"github.com/sdslabs/SWS/lib/configs"
	"github.com/sdslabs/SWS/lib/docker"
	"github.com/sdslabs/SWS/lib/types"
	"github.com/sdslabs/SWS/lib/utils"
)

type context struct {
	Index string   `json:"index" valid:"required"`
	Port  string   `json:"port" valid:"required,port"`
	Args  []string `json:"args"`
}

type pythonRequestBody struct {
	Name          string                 `json:"name" valid:"required,alphanum,stringlength(3|40)"`
	URL           string                 `json:"url" valid:"required,url"`
	Context       context                `json:"context" valid:"required"`
	PythonVersion string                 `json:"python_version" valid:"required"`
	Requirements  string                 `json:"requirements" valid:"required"`
	Django        bool                   `json:"django"`
	Env           map[string]interface{} `json:"env"`
}

func validateRequest(c *gin.Context) {

	var bodyBytes []byte
	if c.Request.Body != nil {
		bodyBytes, _ = ioutil.ReadAll(c.Request.Body)
	}
	// Restore the io.ReadCloser to its original state
	c.Request.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))
	var req pythonRequestBody

	json.Unmarshal(bodyBytes, &req)

	if result, err := validator.ValidateStruct(req); !result {
		c.AbortWithStatusJSON(400, gin.H{
			"error": err,
		})
	} else {
		c.Next()
	}
}

func startServer(index string, args []string, env *types.ApplicationEnv) (string, types.ResponseError) {
	arguments := strings.Join(args, " ")
	serveCmd := fmt.Sprintf(`python %s %s &> /proc/1/fd/1`, index, arguments)
	cmd := []string{"bash", "-c", serveCmd}
	execID, err := docker.ExecDetachedProcess(env.Context, env.Client, env.ContainerID, cmd)
	if err != nil {
		return execID, types.NewResErr(500, "failed to start the server", err)
	}
	return execID, nil
}

func installRequirements(path string, env *types.ApplicationEnv) (string, types.ResponseError) {
	cmd := []string{"bash", "-c", fmt.Sprintf(`pip install -r %s &> /proc/1/fd/1`, path)}
	execID, err := docker.ExecDetachedProcess(env.Context, env.Client, env.ContainerID, cmd)
	if err != nil {
		return execID, types.NewResErr(500, "failed to install requirements", err)
	}
	return execID, nil
}

func pipeline(data map[string]interface{}) types.ResponseError {
	context := data["context"].(map[string]interface{})

	var image string
	if data["python_version"].(string) == "3" {
		image = utils.ServiceConfig["python"].(map[string]interface{})["python3_image"].(string)
	} else {
		image = utils.ServiceConfig["python"].(map[string]interface{})["python2_image"].(string)
	}

	appConf := &types.ApplicationConfig{
		DockerImage:  image,
		ConfFunction: configs.CreatePythonContainerConfig,
	}

	appEnv, resErr := api.SetupApplication(appConf, data)
	if resErr != nil {
		return resErr
	}

	// Path of `requirements.txt` or any-other file containing requirements
	requirements := data["requirements"]
	if requirements != nil {
		_, resErr = installRequirements(requirements.(string), appEnv)
		if resErr != nil {
			return resErr
		}
	}

	if data["django"] != nil {
		if data["django"].(bool) {
			_, resErr = startServer("manage.py", []string{"runserver"}, appEnv)
		}
	} else {
		args := context["args"].([]interface{})
		var arguments []string
		for _, arg := range args {
			arguments = append(arguments, arg.(string))
		}
		_, resErr = startServer(context["index"].(string), arguments, appEnv)
	}
	if resErr != nil {
		return resErr
	}

	return nil
}
