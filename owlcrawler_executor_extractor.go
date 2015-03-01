// +build extractorExec

package main

import (
	"github.com/fmpwizard/owlcrawler/cloudant"
	"github.com/fmpwizard/owlcrawler/parse"
	log "github.com/golang/glog"
	"github.com/iron-io/iron_go/mq"
	exec "github.com/mesos/mesos-go/executor"
	mesos "github.com/mesos/mesos-go/mesosproto"

	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
)

type exampleExecutor struct {
	tasksLaunched int
}

//OwlCrawlMsg is used to decode the Data payload from the framework
type OwlCrawlMsg struct {
	URL       string
	ID        string
	QueueName string
}

var fn = func(url string) bool {
	return !cloudant.IsURLThere(url)
}

func newExampleExecutor() *exampleExecutor {
	return &exampleExecutor{tasksLaunched: 0}
}

func (exec *exampleExecutor) Registered(driver exec.ExecutorDriver, execInfo *mesos.ExecutorInfo, fwinfo *mesos.FrameworkInfo, slaveInfo *mesos.SlaveInfo) {
	log.V(3).Infof("Registered Executor on slave ", slaveInfo.GetHostname())
}

func (exec *exampleExecutor) Reregistered(driver exec.ExecutorDriver, slaveInfo *mesos.SlaveInfo) {
	log.V(3).Infof("Re-registered Executor on slave ", slaveInfo.GetHostname())
}

func (exec *exampleExecutor) Disconnected(exec.ExecutorDriver) {
	log.V(3).Infof("Executor disconnected.")
}

func (exec *exampleExecutor) LaunchTask(driver exec.ExecutorDriver, taskInfo *mesos.TaskInfo) {
	log.V(2).Infof("Launching task", taskInfo.GetName())
	runStatus := &mesos.TaskStatus{
		TaskId: taskInfo.GetTaskId(),
		State:  mesos.TaskState_TASK_RUNNING.Enum(),
	}
	_, err := driver.SendStatusUpdate(runStatus)
	if err != nil {
		log.Errorln("Got error %s\n", err)
	}

	exec.tasksLaunched++
	log.V(2).Infof("Total tasks launched ", exec.tasksLaunched)
	exec.extractText(driver, taskInfo)
}

func (exec *exampleExecutor) extractText(driver exec.ExecutorDriver, taskInfo *mesos.TaskInfo) {

	//Read information about this URL we are about to process
	payload := bytes.NewReader(taskInfo.GetData())
	var queueMessage OwlCrawlMsg
	dec := gob.NewDecoder(payload)
	err := dec.Decode(&queueMessage)
	if err != nil {
		log.Errorln("decode error:", err)
	}
	queue := mq.New(queueMessage.QueueName)
	if queueMessage.URL == "" {
		runStatus := &mesos.TaskStatus{
			TaskId: taskInfo.GetTaskId(),
			State:  mesos.TaskState_TASK_FINISHED.Enum(),
		}
		_, err := driver.SendStatusUpdate(runStatus)
		if err != nil {
			fmt.Printf("Failed to tell mesos that we were done, sorry, got: %v", err)
		}
		_ = queue.DeleteMessage(queueMessage.ID)
		return
	}

	//Fetch stored html and do extraction
	fmt.Printf("/////////////queueMessage.URL %+v\n", queueMessage.URL)

	doc, err := getStoredHTMLForURL(queueMessage.URL)
	if err != nil {
		queue.DeleteMessage(queueMessage.ID)
	} else {
		err = saveExtractedData(extractData(doc))
		if err == cloudant.ERROR_NO_LATEST_VERSION {
			doc, err = getStoredHTMLForURL(queueMessage.URL)
			if err != nil {
				fmt.Printf("Failed to get latest version of %s\n", queueMessage.URL)
				queue.DeleteMessage(queueMessage.ID)
				return
			}
			saveExtractedData(extractData(doc))
		} else if err != nil {
			_ = queue.DeleteMessage(queueMessage.ID)
			runStatus := &mesos.TaskStatus{
				TaskId: taskInfo.GetTaskId(),
				State:  mesos.TaskState_TASK_FAILED.Enum(),
			}
			_, err := driver.SendStatusUpdate(runStatus)
			if err != nil {
				fmt.Printf("Failed to tell mesos that we died, sorry, got: %v", err)
			}
		}
	}
	// finish task
	finStatus := &mesos.TaskStatus{
		TaskId: taskInfo.GetTaskId(),
		State:  mesos.TaskState_TASK_FINISHED.Enum(),
	}
	_, err = driver.SendStatusUpdate(finStatus)
	if err != nil {
		log.Errorln("Got error", err)
	}
	log.V(2).Infof("Task finished", taskInfo.GetName())
}
func extractData(doc cloudant.CouchDoc) cloudant.CouchDoc {
	doc.Text = parse.ExtractText(doc.HTML)
	fetch, storing := parse.ExtractLinks(doc.HTML, doc.URL, fn)
	doc.LinksToQueue = fetch.URL
	doc.Links = storing.URL
	urlToFetchQueue := mq.New("urls_to_fetch")
	for _, u := range fetch.URL {
		urlToFetchQueue.PushString(u)
	}
	return doc
}

func saveExtractedData(doc cloudant.CouchDoc) error {
	jsonDocWithExtractedData, err := json.Marshal(doc)
	if err != nil {
		return errors.New(fmt.Sprintf("Error generating json to save docWithText in database, got: %v\n", err))
	}
	ret, err := cloudant.SaveExtractedTextAndLinks(doc.ID, jsonDocWithExtractedData)
	if err == cloudant.ERROR_NO_LATEST_VERSION {
		return cloudant.ERROR_NO_LATEST_VERSION
	}
	if err != nil {
		fmt.Printf("Error was: %+v\n", err)
		fmt.Printf("Doc was: %+v\n", doc)
		return err
	}
	fmt.Printf("saveExtractedData gave: %+v\n", ret)
	return nil
}

func getStoredHTMLForURL(url string) (cloudant.CouchDoc, error) {
	doc, err := cloudant.GetURLData(url)
	if err == cloudant.ERROR_404 {
		return doc, cloudant.ERROR_404
	}
	if err != nil {
		fmt.Printf("Error was: %+v\n", err)
		fmt.Printf("Doc was: %+v\n", doc)
		return doc, err
	}
	return doc, nil
}

func (exec *exampleExecutor) KillTask(exec.ExecutorDriver, *mesos.TaskID) {
	log.V(3).Infof("Kill task")
}

func (exec *exampleExecutor) FrameworkMessage(driver exec.ExecutorDriver, msg string) {
	log.V(3).Infof("Got framework message: ", msg)
}

func (exec *exampleExecutor) Shutdown(exec.ExecutorDriver) {
	log.V(3).Infof("Shutting down the executor ")
}

func (exec *exampleExecutor) Error(driver exec.ExecutorDriver, err string) {
	log.V(3).Infof("Got error message:", err)
}

// -------------------------- func inits () ----------------- //
func init() {
	flag.Parse()
}

func main() {
	log.V(2).Infof("Starting Extractor Executor")

	dconfig := exec.DriverConfig{
		Executor: newExampleExecutor(),
	}
	driver, err := exec.NewMesosExecutorDriver(dconfig)

	if err != nil {
		log.Errorln("Unable to create a ExecutorDriver ", err.Error())
	}

	_, err = driver.Start()
	if err != nil {
		log.Errorln("Got error:", err)
		return
	}
	driver.Join()
}
