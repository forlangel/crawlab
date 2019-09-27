package routes

import (
	"crawlab/constants"
	"crawlab/database"
	"crawlab/model"
	"crawlab/services"
	"crawlab/utils"
	"github.com/apex/log"
	"github.com/gin-gonic/gin"
	"github.com/globalsign/mgo"
	"github.com/globalsign/mgo/bson"
	"github.com/pkg/errors"
	"github.com/satori/go.uuid"
	"github.com/spf13/viper"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

func GetSpiderList(c *gin.Context) {
	pageNumStr, _ := c.GetQuery("pageNum")
	pageSizeStr, _ := c.GetQuery("pageSize")
	keyword, _ := c.GetQuery("keyword")
	pageNum, _ := strconv.Atoi(pageNumStr)
	pageSize, _ := strconv.Atoi(pageSizeStr)
	skip := pageSize * (pageNum - 1)
	filter := bson.M{"name": bson.M{"$regex": bson.RegEx{Pattern: keyword, Options: "im"}}}
	results, count, err := model.GetSpiderList(filter, skip, pageSize)
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}
	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
		Data:    bson.M{"list": results, "total": count},
	})
}

func GetSpider(c *gin.Context) {
	id := c.Param("id")

	if !bson.IsObjectIdHex(id) {
		HandleErrorF(http.StatusBadRequest, c, "invalid id")
	}

	result, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}
	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
		Data:    result,
	})
}

func PostSpider(c *gin.Context) {
	id := c.Param("id")

	if !bson.IsObjectIdHex(id) {
		HandleErrorF(http.StatusBadRequest, c, "invalid id")
	}

	var item model.Spider
	if err := c.ShouldBindJSON(&item); err != nil {
		HandleError(http.StatusBadRequest, c, err)
		return
	}

	if err := model.UpdateSpider(bson.ObjectIdHex(id), item); err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
	})
}

func PublishSpider(c *gin.Context) {
	id := c.Param("id")

	if !bson.IsObjectIdHex(id) {
		HandleErrorF(http.StatusBadRequest, c, "invalid id")
	}

	spider, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	services.PublishSpider(spider)

	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
	})
}

func PutSpider(c *gin.Context) {
	// 从body中获取文件
	uploadFile, err := c.FormFile("file")
	if err != nil {
		debug.PrintStack()
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 如果不为zip文件，返回错误
	if !strings.HasSuffix(uploadFile.Filename, ".zip") {
		debug.PrintStack()
		HandleError(http.StatusBadRequest, c, errors.New("Not a valid zip file"))
		return
	}

	// 以防tmp目录不存在
	tmpPath := viper.GetString("other.tmppath")
	if !utils.Exists(tmpPath) {
		if err := os.Mkdir(tmpPath, os.ModePerm); err != nil {
			log.Error("mkdir other.tmppath dir error:" + err.Error())
			debug.PrintStack()
			HandleError(http.StatusBadRequest, c, errors.New("Mkdir other.tmppath dir error"))
			return
		}
	}

	// 保存到本地临时文件
	randomId := uuid.NewV4()
	tmpFilePath := filepath.Join(tmpPath, randomId.String()+".zip")
	if err := c.SaveUploadedFile(uploadFile, tmpFilePath); err != nil {
		log.Error("save upload file error: " + err.Error())
		debug.PrintStack()
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	s, gf := database.GetGridFs("files")
	defer s.Close()

	// 判断文件是否已经存在
	var gfFile model.GridFs
	if err := gf.Find(bson.M{"filename": uploadFile.Filename}).One(&gfFile); err == nil {
		// 已经存在文件，则删除
		_ = gf.RemoveId(gfFile.Id)
	}

	// 上传到GridFs
	fid, err := services.UploadToGridFs(uploadFile.Filename, tmpFilePath)
	if err != nil {
		log.Errorf("upload to grid fs error: %s", err.Error())
		debug.PrintStack()
		return
	}

	idx := strings.LastIndex(uploadFile.Filename, "/")
	targetFilename := uploadFile.Filename[idx+1:]

	// 判断爬虫是否存在
	spiderName := strings.Replace(targetFilename, ".zip", "", 1)
	spider := model.GetSpiderByName(spiderName)
	if spider == nil {
		// 保存爬虫信息
		srcPath := viper.GetString("spider.path")
		spider := model.Spider{
			Name:        spiderName,
			DisplayName: spiderName,
			Type:        constants.Customized,
			Src:         filepath.Join(srcPath, spiderName),
			FileId:      fid,
		}
		_ = spider.Add()
	}

	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
	})
}

func DeleteSpider(c *gin.Context) {
	id := c.Param("id")

	if !bson.IsObjectIdHex(id) {
		HandleErrorF(http.StatusBadRequest, c, "invalid id")
		return
	}

	// 获取该爬虫
	spider, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 删除爬虫文件目录
	if err := os.RemoveAll(spider.Src); err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 从数据库中删除该爬虫
	if err := model.RemoveSpider(bson.ObjectIdHex(id)); err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 删除日志文件
	if err := services.RemoveLogBySpiderId(spider.Id); err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 删除爬虫对应的task任务
	if err := model.RemoveTaskBySpiderId(spider.Id); err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
	})
}

func GetSpiderTasks(c *gin.Context) {
	id := c.Param("id")

	spider, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	tasks, err := spider.GetTasks()
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
		Data:    tasks,
	})
}

func GetSpiderDir(c *gin.Context) {
	// 爬虫ID
	id := c.Param("id")

	// 目录相对路径
	path := c.Query("path")

	// 获取爬虫
	spider, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 获取目录下文件列表
	spiderPath := viper.GetString("spider.path")
	f, err := ioutil.ReadDir(filepath.Join(spiderPath, spider.Name, path))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 遍历文件列表
	var fileList []model.File
	for _, file := range f {
		fileList = append(fileList, model.File{
			Name:  file.Name(),
			IsDir: file.IsDir(),
			Size:  file.Size(),
			Path:  filepath.Join(path, file.Name()),
		})
	}

	// 返回结果
	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
		Data:    fileList,
	})
}

func GetSpiderFile(c *gin.Context) {
	// 爬虫ID
	id := c.Param("id")

	// 文件相对路径
	path := c.Query("path")

	// 获取爬虫
	spider, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 读取文件
	fileBytes, err := ioutil.ReadFile(filepath.Join(spider.Src, path))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
	}

	// 返回结果
	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
		Data:    utils.BytesToString(fileBytes),
	})
}

type SpiderFileReqBody struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func PostSpiderFile(c *gin.Context) {
	// 爬虫ID
	id := c.Param("id")

	// 文件相对路径
	var reqBody SpiderFileReqBody
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		HandleError(http.StatusBadRequest, c, err)
		return
	}

	// 获取爬虫
	spider, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 写文件
	if err := ioutil.WriteFile(filepath.Join(spider.Src, reqBody.Path), []byte(reqBody.Content), os.ModePerm); err != nil {
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 返回结果
	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
	})
}

func GetSpiderStats(c *gin.Context) {
	type Overview struct {
		TaskCount            int     `json:"task_count" bson:"task_count"`
		ResultCount          int     `json:"result_count" bson:"result_count"`
		SuccessCount         int     `json:"success_count" bson:"success_count"`
		SuccessRate          float64 `json:"success_rate"`
		TotalWaitDuration    float64 `json:"wait_duration" bson:"wait_duration"`
		TotalRuntimeDuration float64 `json:"runtime_duration" bson:"runtime_duration"`
		AvgWaitDuration      float64 `json:"avg_wait_duration"`
		AvgRuntimeDuration   float64 `json:"avg_runtime_duration"`
	}

	type Data struct {
		Overview Overview              `json:"overview"`
		Daily    []model.TaskDailyItem `json:"daily"`
	}

	id := c.Param("id")

	spider, err := model.GetSpider(bson.ObjectIdHex(id))
	if err != nil {
		log.Errorf(err.Error())
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	s, col := database.GetCol("tasks")
	defer s.Close()

	// 起始日期
	startDate := time.Now().Add(-time.Hour * 24 * 30)
	endDate := time.Now()

	// match
	op1 := bson.M{
		"$match": bson.M{
			"spider_id": spider.Id,
			"create_ts": bson.M{
				"$gte": startDate,
				"$lt":  endDate,
			},
		},
	}

	// project
	op2 := bson.M{
		"$project": bson.M{
			"success_count": bson.M{
				"$cond": []interface{}{
					bson.M{
						"$eq": []string{
							"$status",
							constants.StatusFinished,
						},
					},
					1,
					0,
				},
			},
			"result_count":     "$result_count",
			"wait_duration":    "$wait_duration",
			"runtime_duration": "$runtime_duration",
		},
	}

	// group
	op3 := bson.M{
		"$group": bson.M{
			"_id":              nil,
			"task_count":       bson.M{"$sum": 1},
			"success_count":    bson.M{"$sum": "$success_count"},
			"result_count":     bson.M{"$sum": "$result_count"},
			"wait_duration":    bson.M{"$sum": "$wait_duration"},
			"runtime_duration": bson.M{"$sum": "$runtime_duration"},
		},
	}

	// run aggregation pipeline
	var overview Overview
	if err := col.Pipe([]bson.M{op1, op2, op3}).One(&overview); err != nil {
		if err == mgo.ErrNotFound {
			c.JSON(http.StatusOK, Response{
				Status:  "ok",
				Message: "success",
				Data: Data{
					Overview: overview,
					Daily:    []model.TaskDailyItem{},
				},
			})
			return
		}
		log.Errorf(err.Error())
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	// 后续处理
	successCount, _ := strconv.ParseFloat(strconv.Itoa(overview.SuccessCount), 64)
	taskCount, _ := strconv.ParseFloat(strconv.Itoa(overview.TaskCount), 64)
	overview.SuccessRate = successCount / taskCount
	overview.AvgWaitDuration = overview.TotalWaitDuration / taskCount
	overview.AvgRuntimeDuration = overview.TotalRuntimeDuration / taskCount

	items, err := model.GetDailyTaskStats(bson.M{"spider_id": spider.Id})
	if err != nil {
		log.Errorf(err.Error())
		HandleError(http.StatusInternalServerError, c, err)
		return
	}

	c.JSON(http.StatusOK, Response{
		Status:  "ok",
		Message: "success",
		Data: Data{
			Overview: overview,
			Daily:    items,
		},
	})
}
