package common

import (
	"github.com/gin-gonic/gin"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

type Responder func(*gin.Context, model.Request, model.Result)

func WriteResponse(c *gin.Context, request model.Request, result model.Result) {
	f := request.Filter
	data := result.Data
	truncated := request.List() && len(data) > f.Limit
	if truncated {
		data = data[:f.Limit]
	}
	body := gin.H{"data": data, "from": f.From, "to": f.To, "truncated": truncated}
	if truncated && f.Offset+f.Limit <= 5000 && request.Operation != model.OperationAttributes {
		body["nextOffset"] = f.Offset + f.Limit
	}
	if len(result.Summary) > 0 {
		body["summary"] = result.Summary[0]
	}

	c.JSON(200, body)
}
