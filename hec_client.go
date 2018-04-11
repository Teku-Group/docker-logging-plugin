/*
 * Copyright 2018 Splunk, Inc..
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/Sirupsen/logrus"
)

type hecClient struct {
	client    *http.Client
	transport *http.Transport

	url            string
	healthCheckURL string
	auth           string

	// http compression
	gzipCompression      bool
	gzipCompressionLevel int

	// Advanced options
	postMessagesFrequency time.Duration
	postMessagesBatchSize int
	bufferMaximum         int
}

func (hec *hecClient) postMessages(messages []*splunkMessage, lastChance bool) []*splunkMessage {
	logrus.Infof("Received %d messages.", len(messages))
	messagesLen := len(messages)
	for i := 0; i < messagesLen; i += hec.postMessagesBatchSize {
		upperBound := i + hec.postMessagesBatchSize
		if upperBound > messagesLen {
			upperBound = messagesLen
		}
		if err := hec.tryPostMessages(messages[i:upperBound]); err != nil {
			logrus.Error(err)
			if messagesLen-i >= hec.bufferMaximum || lastChance {
				// If this is last chance - print them all to the daemon log
				if lastChance {
					upperBound = messagesLen
				}
				// Not all sent, but buffer has got to its maximum, let's log all messages
				// we could not send and return buffer minus one batch size
				for j := i; j < upperBound; j++ {
					if jsonEvent, err := json.Marshal(messages[j]); err != nil {
						logrus.Error(err)
					} else {
						logrus.Error(fmt.Errorf("Failed to send a message '%s'", string(jsonEvent)))
					}
				}
				return messages[upperBound:messagesLen]
			}
			// Not all sent, returning buffer from where we have not sent messages
			logrus.Infof("%d messages failed to sent", messagesLen)
			return messages[i:messagesLen]
		}
	}
	// All sent, return empty buffer
	logrus.Infof("%d messages were sent successfully", messagesLen)
	return messages[:0]
}

func (hec *hecClient) tryPostMessages(messages []*splunkMessage) error {
	if len(messages) == 0 {
		logrus.Debug("No message to post")
		return nil
	}
	logrus.Infof("Posint %d messages", len(messages))
	var buffer bytes.Buffer
	var writer io.Writer
	var gzipWriter *gzip.Writer
	var err error
	// If gzip compression is enabled - create gzip writer with specified compression
	// level. If gzip compression is disabled, use standard buffer as a writer
	if hec.gzipCompression {
		gzipWriter, err = gzip.NewWriterLevel(&buffer, hec.gzipCompressionLevel)
		if err != nil {
			return err
		}
		writer = gzipWriter
	} else {
		writer = &buffer
	}
	for _, message := range messages {
		jsonEvent, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err := writer.Write(jsonEvent); err != nil {
			return err
		}
	}
	// If gzip compression is enabled, tell it, that we are done
	if hec.gzipCompression {
		err = gzipWriter.Close()
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequest("POST", hec.url, bytes.NewBuffer(buffer.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", hec.auth)
	// Tell if we are sending gzip compressed body
	if hec.gzipCompression {
		req.Header.Set("Content-Encoding", "gzip")
	}
	res, err := hec.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var body []byte
		body, err = ioutil.ReadAll(res.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s: failed to send event - %s - %s", driverName, res.Status, body)
	}
	io.Copy(ioutil.Discard, res.Body)
	return nil
}

func (hec *hecClient) verifySplunkConnection(l *splunkLogger) error {
	req, err := http.NewRequest(http.MethodGet, hec.healthCheckURL, nil)
	if err != nil {
		return err
	}
	res, err := hec.client.Do(req)
	if err != nil {
		return err
	}
	if res.Body != nil {
		defer res.Body.Close()
	}
	if res.StatusCode != http.StatusOK {
		var body []byte
		body, err = ioutil.ReadAll(res.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s: failed to verify connection - %s - %s", driverName, res.Status, body)
	}
	return nil
}
