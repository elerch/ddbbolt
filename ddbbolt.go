package main

import (
	"bytes"
	"golang.org/x/crypto/sha3"
	"encoding/json"
	"encoding/gob"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"github.com/boltdb/bolt"
)

//////////////////////////////////////////////////////////////////////
// BatchWriteItem Structs
//////////////////////////////////////////////////////////////////////

type batchWriteItemRequest struct {
	tables map[string]batchWriteItemTable
	// TODO: Support ReturnConsumedCapacity
	// TODO: Support ReturnItemCollectionMetrics
}

type batchWriteItemTable struct {
	tableName string
	requests []writeRequest
}

type writeRequest struct {
	deleteRequest []attributeInfo // Key
	putRequest []attributeInfo // Item
}

//////////////////////////////////////////////////////////////////////
// BatchGetItem Structs
//////////////////////////////////////////////////////////////////////

// {"RequestItems": {"etags": {"Keys": [{"PK": {"S": "my key"}}]}}}

type batchGetItemRequest struct {
	tables map[string]batchGetItemTable
	// TODO: Support ReturnConsumedCapacity
}

type batchGetItemTable struct {
	tableName string
	// TODO: Support AttributesToGet
	// TODO: Support ConsistentRead
	// TODO: Support ExpressionAttributeNames
	keys [][]attributeInfo
	// TODO: Support ProjectionExpression
}

//////////////////////////////////////////////////////////////////////
// Common structs
//////////////////////////////////////////////////////////////////////

type attributeInfo struct {
	AttributeName string
	AttributeType string
	AttributeValue interface{}
}

type httpErrorStruct struct {
	msg string
	statusCode int
}

// newHttpError is used for when we need to return a specific error
// back to the customer. Examples are the table not found, which returns
// a 400 status code along with a JSON body to describe the table(s) that
// were missing
func newHttpError(msg string, statusCode int) error {
	return &httpErrorStruct {
		msg: msg,
		statusCode: statusCode,
	}
}

func (errorStruct *httpErrorStruct) Error() string {
	return errorStruct.msg
}

func (errorStruct *httpErrorStruct) StatusCode() int {
	return errorStruct.statusCode
}

//////////////////////////////////////////////////////////////////////
// CreateTable Structs
//////////////////////////////////////////////////////////////////////

type createTableRequest struct {
	TableName string
	KeySchema []keySchemaInfo
	AttributeDefinitions map[string]string
}

type keySchemaInfo struct {
	AttributeName string
	KeyType string
}

//////////////////////////////////////////////////////////////////////
// Common functions
//////////////////////////////////////////////////////////////////////

// getCompositeKey creates a hash from the key information that can be used
// to read/write from the bucket (DDB calls this a table). Keys in DDB
// can be composed of multiple attributes, so we leverage the metadata captured
// in a CreateTable call to lookup the KeySchema, find all the attributes, and
// concatenate the hashes of each attribute value. There are limitations to
// the implementation below. First, we're only currently supporting string
// values. This is probably not that big of a deal to expand. Secondly, we
// only support a single hash implementation, hardcoded to ShakeSum256. Third,
// we only support HASH keys - if a range comes along, this will return an
// error. Lastly, there's no coverage for the case where a client fails to
// send in the whole key. In this case, a hash will be generated for only
// part of the key, and likely no data will be found (read) or an orphan will
// be created (write)
func getCompositeKey(tableName string, db *bolt.DB, key []attributeInfo) ([]byte, error){
	var rc []byte
	var metadata createTableRequest
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("_m"))
		if b == nil {
			log.Printf("Metadata bucket not found!\n")
			return errors.New("Metadata bucket '_m' not found")
		}
		v := b.Get([]byte(tableName))
		if v == nil {
			msg := fmt.Sprintf("No metadata found for table '%s'\n")
			log.Printf(msg)
			return errors.New(msg)
		}
		buf := bytes.NewReader(v)
		dec := gob.NewDecoder(buf)
		err := dec.Decode(&metadata)
		if err != nil { log.Println(`failed gob Decode`, err); return err }
		return nil
	})
	if err != nil {
		log.Println("Error getting metadata: %s", err)
		return rc, err
	}
	if len(metadata.KeySchema) == 0 {
		return rc, errors.New("Error getting metadata: key schema length 0")
	}
	rc = make([]byte, 512 / 8 * len(metadata.KeySchema))
	for i, keypart := range metadata.KeySchema {
		if keypart.KeyType != "HASH" {
			return rc, errors.New("Unsupported key type " + keypart.KeyType + " for attribute " + keypart.AttributeName)
		}
		// TODO: Determine how we could support (if we want to support) other hash types
		// We're simply going to stitch together hashes of these attributes
		for _, attribute := range key {
			if attribute.AttributeName == keypart.AttributeName {
				var buf []byte
				switch attribute.AttributeType {
				case "S":
					buf = []byte(attribute.AttributeValue.(string))
				default:
					return rc, errors.New("Unsupported attribute type " + attribute.AttributeType + " for attribute " + keypart.AttributeName)
				}
				h := make([]byte, 64)
				sha3.ShakeSum256(h, buf)
				copy(rc[(i * 64):], h)
				break;
			}
		}
	}
	return rc, err
}

// parseAttributes is useful for anything that is a set of objects of the form
// {
//   "attribute1": { "S": "foo" },
//   "attribute2": { "S": "bar" }
// }
// This pattern occurs all over the place in DDB. In the above example,
// "attribute1" is the attribute name, "S" is the type, and "foo" is the value
func parseAttributes(attributes map[string]interface{}) []attributeInfo {
	rc := make([]attributeInfo, len(attributes))
	jnx := 0

	for attributename, attributeValueInt := range attributes {
		// { PK: {"S": "url"}}
		var attributetype string
		var attributevalue interface{}
		for attributetype, attributevalue = range attributeValueInt.(map[string]interface{}) {
			// {"S": "url"}
			// Should only be one value - if it's more, do we care?
		}
		rc[jnx] = attributeInfo{
			AttributeName: attributename,
			AttributeType: attributetype,
			AttributeValue: attributevalue,
		}
		jnx++
	}
	return rc
}

//////////////////////////////////////////////////////////////////////
// BatchGetItem implementation
//////////////////////////////////////////////////////////////////////

// We don't use the built-in parse-to-struct mechanism as the REST API
// structures aren't really aligned with what go needs here. We also don't
// need quite the complexity that DDB is using, so we make a few shortcuts
func parseBatchGetItem(event map[string]interface{}) (req batchGetItemRequest, err error) {
	defer func() {
		// recover from panic if one occured. Set err to nil otherwise.
		if paniced := recover(); paniced != nil {
			log.Printf("Parse error: %v", paniced)
			err = errors.New("Error parsing request for batchGetItemRequest")
		}
	}()
	req = batchGetItemRequest {
		tables: map[string]batchGetItemTable{},
	}
	for table, requestInt := range event["RequestItems"].(map[string]interface{}) {
		request := requestInt.(map[string]interface{})
		keys := request["Keys"].([]interface{})
		req.tables[table] = batchGetItemTable{
			tableName: table,
			keys: make([][]attributeInfo, len(keys)),
		}

		// TODO: Support AttributesToGet
		// TODO: Support ConsistentRead
		// TODO: Support ExpressionAttributeNames
		for inx, keyInt := range keys {
			// [{ "PK": {"S": "url"}}, {"PK": {"S": "url2"}}]
			attributes := keyInt.(map[string]interface{})
			req.tables[table].keys[inx] = parseAttributes(attributes)
		}

		// TODO: Support ProjectionExpression
	}
	// TODO: Support ReturnConsumedCapacity
	return req, nil
}

// batchGetItem does the heavy lifting. We'll get parseBatchGetItem to parse
// out the request, then loop through tables to get data. For each key
// (collection of attributes), we'll get the compositeKey hash, then go to
// bolt to find the data. If there is data in the DB, we use gob to decode it.
// Gob will provide us with an []attributeInfo, so we need to coerce that
// structure into what the response should look like, then json.Marshal that
// to the correct string. From there, it's basically a bunch of error
// handling and string concatenation.
func batchGetItem(event map[string]interface{}, db *bolt.DB) (string, error) {
	req, err := parseBatchGetItem(event)
	if err != nil {
		log.Println("Error parsing")
		return "", err
	}
	consumedCapacity := ""
	response := ""
	prefix := ""
	for _, table := range req.tables {
		log.Printf("got request for table %s\n", table.tableName)
		keyResponses := ""
		keyPrefix := ""
		err = db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte(table.tableName))
			if b == nil {
				log.Printf("Table '%s' does not exist", table.tableName)
				msg := fmt.Sprintf("{\"__type\":\"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException\",\"message\":\"Requested resource not found: Table: %s not found\"}", table.tableName)
				return newHttpError(msg, 400)
			}
			for _, key := range table.keys {
				compositeKey, err := getCompositeKey(table.tableName, db, key)
				if err != nil {
					log.Printf("Error getting composite key: %s", err)
					return err
				}
				v := b.Get(compositeKey)
				if v != nil {
					buf := bytes.NewReader(v)
					dec := gob.NewDecoder(buf)
					var attributes []attributeInfo
					err = dec.Decode(&attributes)
					if err != nil {
						log.Printf("Error decoding value for key: %x", compositeKey)
						return err
					}
					respObj := map[string]interface{}{}
					for _, attribute := range attributes {
						typedVal := map[string]interface{}{}
						typedVal[attribute.AttributeType] = attribute.AttributeValue
						respObj[attribute.AttributeName] = typedVal
					}
					respStr, err := json.Marshal(respObj)
					if err != nil {
						log.Printf("Error marshalling value to json for key: %x", compositeKey)
						return err
					}
					// This is only useful for debugging and we probably don't
					// want all our values showing up in logs
					// log.Printf("Got value '%s' for key '%s' with hash '%x'", respStr, key, compositeKey)
					// We're expecting the value to be a json doc, so this becomes simple
					keyResponses = fmt.Sprintf(`%s%s%s`, keyResponses, prefix, respStr)
					keyPrefix = ","
				}else{
					log.Printf("No value found for key '%s' with hash '%x'", key, compositeKey)
				}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		response = fmt.Sprintf(`%s%s"%s": [ %s ]`, response, prefix, table.tableName, keyResponses)
		consumedCapacity = fmt.Sprintf(`%s%s{"TableName": "%s", "CapacityUnits": 1 }`,
								   consumedCapacity, prefix, table.tableName)
		prefix = ","
	}
	// TODO: I think this implementation is incomplete
	msg := fmt.Sprintf(`
 {
    "Responses": { %s },
    "UnprocessedKeys": {},
    "ConsumedCapacity": [ %s ]
}
	`, response, consumedCapacity)
	return msg, nil
}

//////////////////////////////////////////////////////////////////////
// CreateTable implementation
//////////////////////////////////////////////////////////////////////

func parseCreateTable(event map[string]interface{}) (req createTableRequest, err error) {
	// {"TableName": "etags", "AttributeDefinitions": [{"AttributeName": "PK", "AttributeType": "S"}], "KeySchema": [{"AttributeName": "PK", "KeyType": "HASH"}]}
	defer func() {
		// recover from panic if one occured. Set err to nil otherwise.
		if paniced := recover(); paniced != nil {
			log.Printf("Parse error: %v", paniced)
			err = errors.New("Error parsing request for batchGetItemRequest")
		}
	}()
	req = createTableRequest {}
	// TODO: Support everything else
	req.TableName = event["TableName"].(string)
	keySchema := event["KeySchema"].([]interface{})
	req.KeySchema = make([]keySchemaInfo, len(keySchema))
	req.AttributeDefinitions = map[string]string{}
	for _, definitionInfo := range event["AttributeDefinitions"].([]interface{}) {
		definition := definitionInfo.(map[string]interface{})
		req.AttributeDefinitions[definition["AttributeName"].(string)] = definition["AttributeType"].(string)
	}
	for inx, definitionInfo := range keySchema {
		definition := definitionInfo.(map[string]interface{})
		req.KeySchema[inx] = keySchemaInfo {
			AttributeName: definition["AttributeName"].(string),
			KeyType: definition["KeyType"].(string),
		}
	}
	return req, nil
}

// We need a metadata bucket to hold key information from this request, as
// DDB supports composite keys (keys with > 1 attribute). Note that multiple
// attribute keys have not been tested. The metadata bucket will be created
// if it doesn't already exist
func createTableMetadata(req createTableRequest, db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		var err error
		b := tx.Bucket([]byte("_m"))
		if b == nil {
			log.Printf("Metadata bucket doesn't exist - creating")
			b, err = tx.CreateBucket([]byte("_m"))
			if err != nil {
				log.Printf("Error creating metadata bucket: %s", req.TableName, err.Error())
				return err
			}
		}
		// Insert data into metadata bucket
		buf := bytes.Buffer{}
		enc := gob.NewEncoder(&buf)
		err = enc.Encode(req)
		if err != nil { log.Println(`failed gob Encode`, err); return err }
		err = b.Put([]byte(req.TableName), buf.Bytes())
		return err
	})
}

// createTable. This is fairly straightforward, with a bolt bucket = DDB table
func createTable(event map[string]interface{}, db *bolt.DB) (string, error) {
	req, err := parseCreateTable(event)
	if err != nil {
		log.Println("Error parsing")
		return "", err
	}
	if err = createTableMetadata(req, db); err != nil {
		log.Printf("Error creating metadata for bucket '%s'\n", err.Error())
		return "", err
	}
	// Use the transaction...
	// we only care about table name, and table = bucket in bolt
	// Start a writable transaction.
	tx, err := db.Begin(true)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	_, err = tx.CreateBucket([]byte(req.TableName))
	if err != nil {
		log.Printf("Error creating bucket '%s': %s", req.TableName, err.Error())
		return "", err
	}

	// Commit the transaction and check for error.
	if err := tx.Commit(); err != nil {
		return "", err
	}

	msg := fmt.Sprintf(`{
    "AttributeDefinitions": [],
    "TableName": "%s",
    "KeySchema": [],
    "LocalSecondaryIndexes": []
    "ProvisionedThroughput": {
        "ReadCapacityUnits": 5000,
        "WriteCapacityUnits": 5000
    },
    "Tags": []
}
`, req.TableName)
	return msg, nil
}

//////////////////////////////////////////////////////////////////////
// BatchWriteItem implementation
//////////////////////////////////////////////////////////////////////

func parseBatchWriteItem(event map[string]interface{}) (req batchWriteItemRequest, err error) {
	defer func() {
		// recover from panic if one occured. Set err to nil otherwise.
		if paniced := recover(); paniced != nil {
			log.Printf("Parse error: %v", paniced)
			err = errors.New("Error parsing request for batchWriteItemRequest")
		}
	}()
	req = batchWriteItemRequest {
		tables: map[string]batchWriteItemTable{},
	}
	for table, tableInt := range event["RequestItems"].(map[string]interface{}) {
		requests := tableInt.([]interface{})
		req.tables[table] = batchWriteItemTable{
			tableName: table,
			requests: make([]writeRequest, len(requests)),
		}

		log.Println("parsing writeitem request for table: " + table)
		for inx, requestInt := range requests {
			// [{{ "DeleteRequest": {...}, {"PutRequest": {...}}}]
			request := requestInt.(map[string]interface{})
			putRequest, putok := request["PutRequest"]
			deleteRequest, delok := request["DeleteRequest"]
			if putok {
				req.tables[table].requests[inx].putRequest =
					parseAttributes(putRequest.(map[string]interface{})["Item"].(map[string]interface{}))
			}
			if delok {
				req.tables[table].requests[inx].deleteRequest =
					parseAttributes(deleteRequest.(map[string]interface{})["Key"].(map[string]interface{}))
			}
		}

		// TODO: Support ProjectionExpression
	}
	// TODO: Support ReturnConsumedCapacity
	return req, nil
}

// {"RequestItems": {"etags": [{"PutRequest": {"Item": {"PK": {"S": "foo"}, "etag": {"S": "bar"}}}}]}}

// batchWriteItem gets the parsed event, loops through each table and processes
// any requests that might exist. This DOES NOT CURRENTLY DO DELETES, although
// that should be a trivial implementation. The key is the compositekey hash,
// and the value is the gob-encoded []attributeInfo for the passed in data
func batchWriteItem(event map[string]interface{}, db *bolt.DB) (string, error) {
	req, err := parseBatchWriteItem(event)
	if err != nil {
		log.Println("Could not parse BatchWriteItem: " + err.Error())
		return "", err
	}
	for tablename, table := range req.tables {
		log.Println("Processing request for table " + tablename)
		for _, request := range table.requests {
			if request.putRequest != nil && len(request.putRequest) > 0 {
				key, err := getCompositeKey(tablename, db, request.putRequest)
				if err != nil {
					log.Println("Error getting composite key: " + err.Error())
					return "", err
				}
				log.Printf("Put request for hashkey %x", key)
				err = db.Update(func(tx *bolt.Tx) error {
					var err error
					b := tx.Bucket([]byte(tablename))
					if b == nil {
						log.Printf("Table '%s' does not exist", table.tableName)
						msg := fmt.Sprintf("{\"__type\":\"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException\",\"message\":\"Requested resource not found: Table: %s not found\"}", table.tableName)
						return newHttpError(msg, 400)
					}
					// Insert data into bucket
					buf := bytes.Buffer{}
					enc := gob.NewEncoder(&buf)
					err = enc.Encode(request.putRequest)
					if err != nil { log.Println(`failed gob Encode`, err); return err }
					err = b.Put([]byte(key), buf.Bytes())
					return err
				})
				if err != nil {
					log.Println("Error updating database: " + err.Error())
					return "", err
				}
				log.Printf("Put update succeeded for key %x", key)
			}
			if request.deleteRequest != nil && len(request.deleteRequest) > 0 {
				return "", errors.New("Delete requests not yet supported")
			}
		}
	}

	msg := fmt.Sprintf(`
 {
    "ConsumedCapacity": [],
    "ItemCollectionMetrics": {},
    "UnprocessedItems": {}
}`)
	return msg, nil
}

//////////////////////////////////////////////////////////////////////
// Web goo
//////////////////////////////////////////////////////////////////////

// postCommand handles routing requests to the appropriate function based
// on the command (X-Amz-Target http header)
func postCommand(command string, rawEvent []byte, db *bolt.DB) (string, error) {
	// Probably better as map[string, func]
	var event map[string]interface{}
	if err := json.Unmarshal(rawEvent, &event); err !=nil {
		return "", err
	}
	switch command {
	case "DynamoDB_20120810.BatchGetItem":
		return batchGetItem(event, db)
	case "DynamoDB_20120810.CreateTable":
		return createTable(event, db)
	case "DynamoDB_20120810.BatchWriteItem":
		return batchWriteItem(event, db)
	default:
		return "", errors.New("unrecognized command: " + command)
	}
	return "", errors.New("unreachable")

}

// main - setup http listener and marshall all posts to postCommand for
// processing. Will also open up/create our bolt db
func main() {
	port := os.Getenv("PORT")
	if port == "" { port = ":8080" } else { port = ":" + port }
	filename := os.Getenv("FILE")
	if filename == "" { filename = "ddb.db" }
	db, err := bolt.Open(filename, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	fmt.Printf("listening on port %s\n", port)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			body, err := ioutil.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "Could not read body", 400)
				return
			}
			target := r.Header.Get("X-Amz-Target")
			log.Println(target)
			// Dump headers (if needed)
			// for k, v := range r.Header {
			// 	log.Println(k)
			// 	log.Println(v)
			// }
			if os.Getenv("DEBUG") == "true" {
				// This could include sensitive data
				log.Println(string(body[:]))
			}
			var resp = ""
			if resp, err = postCommand(target, body, db); err != nil {
				switch err.(type) {
				case *httpErrorStruct:
					httpError := err.(*httpErrorStruct)
					w.WriteHeader(httpError.StatusCode())
					if _, err = w.Write([]byte(httpError.Error())); err != nil {
						http.Error(w, "Could not write response", 400)
						return
					}
				default:
					http.Error(w, "Bad request", 400)
				}
				return
			}
			if _, err = w.Write([]byte(resp)); err != nil {
				http.Error(w, "Could not write response", 400)
			}
		default:
			fmt.Printf("invalid request, method  %s\n", r.Method)
			http.Error(w, "Not found", 404)
			return
		}
	})

	log.Fatal(http.ListenAndServe(port, nil))
}

