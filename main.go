package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/rekognition"
	"github.com/husobee/vestigo"
	"github.com/jawher/mow.cli"
	"github.com/lytics/multibayes"
	log "github.com/sirupsen/logrus"
)

var s3bucketName = "com.ft.imagepublish.k8s-content-test.test"

const (
	about    = "http://www.ft.com/ontology/annotation/about"
	mentions = "http://www.ft.com/ontology/annotation/mentions"
)

func main() {
	app := cli.App("pac-aurora-backup", "A backup app for PAC Aurora clusters")

	awsRegion := app.String(cli.StringOpt{
		Name:   "aws-region",
		Desc:   "The AWS region of the Aurora cluster that needs a backup",
		EnvVar: "AWS_REGION",
	})

	awsAccessKeyID := app.String(cli.StringOpt{
		Name:   "aws-access-key-id",
		Desc:   "The access key ID to access AWS",
		EnvVar: "AWS_ACCESS_KEY_ID",
	})

	awsSecretAccessKey := app.String(cli.StringOpt{
		Name:   "aws-secret-access-key",
		Desc:   "The secret access key to access AWS",
		EnvVar: "AWS_SECRET_ACCESS_KEY",
	})

	apiKey := app.String(cli.StringOpt{
		Name:   "api-key",
		Desc:   "API key",
		EnvVar: "API_KEY",
	})

	app.Action = func() {

		var classifier *multibayes.Classifier

		sess, err := session.NewSession(&aws.Config{
			Region:      aws.String(*awsRegion),
			Credentials: credentials.NewStaticCredentials(*awsAccessKeyID, *awsSecretAccessKey, ""),
		})

		if err != nil {
			panic(err)
		}

		reko := rekognition.New(sess)

		if _, err := os.Stat("./classifier.json"); os.IsNotExist(err) {
			log.Info("Building classifier...")
			trainingSet := buildTrainingSet(reko, *apiKey)
			classifier = multibayes.NewClassifier()
			classifier.MinClassSize = 0
			for _, item := range trainingSet {
				classifier.Add(item.text, item.concepts)
			}
			bytes, err := json.Marshal(classifier)
			if err != nil {
				log.WithError(err).Error("Error in marshalling the classifier")
				panic(err)
			}
			err = ioutil.WriteFile("./classifier.json", bytes, 0664)
			if err != nil {
				log.WithError(err).Error("Error in writing classifier to filesystem")
				panic(err)
			}
			log.WithField("training_set_size", len(trainingSet)).Info("Classifier built!")

		} else {
			classifier, err = multibayes.LoadClassifierFromFile("./classifier.json")
			if err != nil {
				log.WithError(err).Error("Error in loading classifier to filesystem")
				panic(err)
			}
		}

		handler := suggestionHandler{reko, classifier, *apiKey}
		router := vestigo.NewRouter()
		router.Get("/content/:uuid/suggest", handler.ServeHTTP)
		log.Fatal(http.ListenAndServe(":8080", router))
	}

	err := app.Run(os.Args)
	if err != nil {
		log.WithError(err).Error("App could not start")
		return
	}

}

func buildTrainingSet(reko *rekognition.Rekognition, apiKey string) []trainingItem {
	var trainingSet []trainingItem
	uuids := getGraphicsUUIDs()

	for _, uuid := range uuids {
		text, err := extractText(reko, uuid)
		if err != nil {
			log.WithError(err).Error("Error in using Rekognition")
			continue
		}

		concepts, err := getAnnotationConcepts(uuid, apiKey)
		if err != nil {
			log.WithError(err).Error("Error in getting annotations")
			continue
		}
		item := trainingItem{text, concepts}
		trainingSet = append(trainingSet, item)

	}
	return trainingSet
}

func getAnnotationConcepts(uuid, apiKey string) ([]string, error) {
	time.Sleep(50 * time.Millisecond)
	req, err := http.NewRequest(http.MethodGet, "http://test.api.ft.com/content/"+uuid+"/annotations", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var annotations []annotation
	err = json.NewDecoder(resp.Body).Decode(&annotations)
	if err != nil {
		return nil, err
	}

	var concepts []string

	for _, a := range annotations {
		if a.Predicate == mentions || a.Predicate == about {
			conceptUUID := a.Id[strings.LastIndex(a.Id, "/")+1:]
			concepts = append(concepts, conceptUUID)
		}
	}
	return concepts, nil
}

func extractText(reko *rekognition.Rekognition, uuid string) (string, error) {
	time.Sleep(50 * time.Millisecond)
	s3obj := &rekognition.S3Object{Bucket: &s3bucketName, Name: &uuid}
	img := &rekognition.Image{S3Object: s3obj}
	input := &rekognition.DetectTextInput{Image: img}

	output, err := reko.DetectText(input)
	if err != nil {
		return "", err
	}

	result := ""

	for _, textDetection := range output.TextDetections {
		if *textDetection.Type == "LINE" {
			result += *textDetection.DetectedText + " "
		}
	}
	return result, nil
}

func getGraphicsUUIDs() []string {
	file, err := ioutil.ReadFile("./graphics_uuids.json")
	if err != nil {
		panic(err)
	}
	var uuids []string
	json.Unmarshal(file, &uuids)
	return uuids
}

type suggestionHandler struct {
	reko       *rekognition.Rekognition
	classifier *multibayes.Classifier
	apiKey     string
}

func (h *suggestionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	contentUUID := vestigo.Param(r, "uuid")
	text, err := extractText(h.reko, contentUUID)
	if err != nil {
		log.WithError(err).Error()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	probs := h.classifier.Posterior(text)

	var conceptsIDs []string

	for conceptID, probability := range probs {
		if probability >= 0.00000001 {
			conceptsIDs = append(conceptsIDs, conceptID)
		}
	}

	if len(conceptsIDs) == 0 {
		http.Error(w, "no concepts", http.StatusNotFound)
		return
	}

	concepts, err := h.retrieveConcepts(conceptsIDs)
	if err != nil {
		log.WithError(err).Error()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bytes, err := json.Marshal(SuggestionsResponse{concepts})
	if err != nil {
		log.WithError(err).Error()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(bytes)
}

func (h *suggestionHandler) retrieveConcepts(conceptsIDs []string) ([]Concept, error) {

	requestURL := "http://test.api.ft.com/internalconcordances?"

	for _, conceptsID := range conceptsIDs {
		requestURL += "ids=" + conceptsID + "&"
	}

	requestURL = requestURL[:len(requestURL)-1]

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("x-api-key", h.apiKey)
	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status from internal concordances: %v", resp.StatusCode)
	}
	var internalConcordancesResp InternalConcordancesResponse
	err = json.NewDecoder(resp.Body).Decode(&internalConcordancesResp)
	if err != nil {
		return nil, err
	}
	var concepts []Concept
	for _, c := range internalConcordancesResp.Concepts {
		c.Predicate = mentions
		concepts = append(concepts, c)
	}
	return concepts, nil
}
