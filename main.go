// main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

type SensorData struct {
	DeviceID  string    `json:"device_id" bson:"device_id"`
	Payload   string    `json:"payload" bson:"payload"`
	Timestamp time.Time `json:"timestamp" bson:"timestamp"`
}

var mongoClient *mongo.Client
var dataCollection *mongo.Collection

func connectMongo() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mongoUser := os.Getenv("MONGO_USER")
	mongoPass := os.Getenv("MONGO_PASS")
	mongoHost := os.Getenv("MONGO_HOST")
	mongoPort := os.Getenv("MONGO_PORT")
	mongoDB := os.Getenv("MONGO_DATABASE")
	mongoCol := os.Getenv("MONGO_COLLECTION")

	uri := fmt.Sprintf("mongodb://%s:%s@%s:%s", mongoUser, mongoPass, mongoHost, mongoPort)
	clientOpts := options.Client().ApplyURI(uri).SetWriteConcern(writeconcern.New(writeconcern.WMajority()))

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		log.Fatalf("[MongoDB] Connection error: %v", err)
	}
	mongoClient = client
	db := mongoClient.Database(mongoDB)
	dataCollection = db.Collection(mongoCol)
	fmt.Printf("[MongoDB] Connected to %s.%s\n", mongoDB, mongoCol)
}

func storeToMongo(data SensorData) {
	encryption := os.Getenv("ENCRYPTION")
	if strings.ToLower(encryption) == "true" {
		cipherAPI := os.Getenv("ENCRYPT_API_URL")
		if cipherAPI == "" {
			log.Println("[CipherAPI] Encryption enabled but API URL not set.")
			return
		}

		payload := fmt.Sprintf(`{"text": "%s"}`, data.Payload)
		req, err := http.NewRequest("POST", cipherAPI+"encrypt", strings.NewReader(payload))
		if err != nil {
			log.Printf("[CipherAPI] Request creation failed: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[CipherAPI] Request failed: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("[CipherAPI] Non-200 response: %d", resp.StatusCode)
			return
		}

		var result struct {
			Result string `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			log.Printf("[CipherAPI] Decode failed: %v", err)
			return
		}

		data.Payload = result.Result
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := dataCollection.InsertOne(ctx, data)
	if err != nil {
		log.Printf("[MongoDB] Insert failed: %v", err)
		return
	}
	fmt.Println("[MongoDB] Data stored.")
}

func messageHandler(client mqtt.Client, msg mqtt.Message) {
	topicParts := strings.Split(msg.Topic(), "/")
	deviceID := topicParts[len(topicParts)-1]

	data := SensorData{
		DeviceID:  deviceID,
		Payload:   string(msg.Payload()),
		Timestamp: time.Now(),
	}
	fmt.Printf("[MQTT] Received from %s: %s\n", deviceID, data.Payload)
	storeToMongo(data)
}

func main() {
	connectMongo()

	mqttBroker := os.Getenv("MQTT_BROKER")
	mqttPort := os.Getenv("MQTT_PORT")
	mqttTopic := os.Getenv("MQTT_TOPIC")
	mqttUser := os.Getenv("MQTT_USERNAME")
	mqttPass := os.Getenv("MQTT_PASSWORD")

	if mqttPort == "" {
		mqttPort = "1883"
	}
	if mqttTopic == "" {
		mqttTopic = "mesh/data/"
	}

	opts := mqtt.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%s", mqttBroker, mqttPort)).
		SetClientID("mqtt-orchestrator").
		SetCleanSession(true)

	if mqttUser != "" {
		opts.SetUsername(mqttUser)
	}
	if mqttPass != "" {
		opts.SetPassword(mqttPass)
	}

	opts.OnConnect = func(c mqtt.Client) {
		fmt.Println("[MQTT] Connected to broker.")
		if token := c.Subscribe(mqttTopic+"#", 0, messageHandler); token.Wait() && token.Error() != nil {
			log.Fatalf("[MQTT] Subscribe error: %v", token.Error())
		}
	}

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("[MQTT] Connection failed: %v", token.Error())
	}

	select {} // keep running
}
