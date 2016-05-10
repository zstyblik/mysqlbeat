package beater

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/beats/libbeat/beat"
	"github.com/elastic/beats/libbeat/cfgfile"
	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"

	"github.com/adibendahan/mysqlbeat/config"

	// mysql go driver
	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultPeriod        = 10 * time.Second
	defaultMySQLHost     = "localhost"
	defaultMySQLPort     = 3306
	defaultMySQLUser     = "mysqlbeat_user"
	defaultMySQLPass     = "mysqlbeat_password"
	defaultDeltaWildCard = "__DELTA"
)

// Mysqlbeat  is a struct tol hold the beat config & info
type Mysqlbeat struct {
	Beat          *beat.Beat
	beatConfig    *config.Config
	done          chan struct{}
	period        time.Duration
	hostname      string
	port          int64
	username      string
	password      string
	queries       []string
	querytypes    []string
	deltawildcard string

	oldvalues    common.MapStr
	oldvaluesage common.MapStr
}

// New Creates beater
func New() *Mysqlbeat {
	return &Mysqlbeat{}
}

/// *** Beater interface methods ***///

// Config is a function to read config file
func (bt *Mysqlbeat) Config(b *beat.Beat) error {
	// Load beater beatConfig
	err := cfgfile.Read(&bt.beatConfig, "")
	if err != nil {
		return fmt.Errorf("Error reading config file: %v", err)
	}

	bt.oldvalues = common.MapStr{"mysqlbeat": "init"}
	bt.oldvaluesage = common.MapStr{"mysqlbeat": "init"}

	if bt.beatConfig.Mysqlbeat.Queries != nil {
		bt.queries = *bt.beatConfig.Mysqlbeat.Queries
	} else {
		bt.queries = []string{}
	}

	if len(bt.queries) < 1 {
		err := fmt.Errorf("there are no queries to execute")
		return err
	}

	if bt.beatConfig.Mysqlbeat.QueryTypes != nil {
		bt.querytypes = *bt.beatConfig.Mysqlbeat.QueryTypes
	} else {
		bt.querytypes = []string{}
	}

	if len(bt.queries) != len(bt.querytypes) {
		err := fmt.Errorf("error on config file, queries array length != querytypes array length (each query should have a corresponding type on the same index)")
		return err
	}

	logp.Info("Total # of queries to execute: %d", len(bt.queries))

	for index, queryStr := range bt.queries {
		logp.Info("Query #%d (type: %s): %s", index+1, bt.querytypes[index], queryStr)
	}

	if bt.beatConfig.Mysqlbeat.Period != nil {
		bt.period = time.Duration(*bt.beatConfig.Mysqlbeat.Period) * time.Second
	} else {
		bt.period = defaultPeriod
	}

	if bt.beatConfig.Mysqlbeat.DeltaWildCard != nil {
		bt.deltawildcard = *bt.beatConfig.Mysqlbeat.DeltaWildCard
	} else {
		bt.deltawildcard = defaultDeltaWildCard
	}

	if bt.beatConfig.Mysqlbeat.Hostname != nil {
		bt.hostname = *bt.beatConfig.Mysqlbeat.Hostname
	} else {
		bt.hostname = defaultMySQLHost
		logp.Info("Hostname not selected, proceeding with '%v' as default",
			bt.hostname)
	}

	if bt.beatConfig.Mysqlbeat.Username != nil {
		bt.username = *bt.beatConfig.Mysqlbeat.Username
	} else {
		bt.username = defaultMySQLUser
		logp.Info("Username not selected, proceeding with '%v' as default",
			defaultMySQLUser)
	}

	if bt.beatConfig.Mysqlbeat.Password != nil {
		bt.password = *bt.beatConfig.Mysqlbeat.Password
	} else {
		bt.password = defaultMySQLPass
		logp.Info("Password not selected, proceeding with '%v' as default",
			defaultMySQLPass)
	}

	if bt.beatConfig.Mysqlbeat.Port != nil {
		bt.port = *bt.beatConfig.Mysqlbeat.Port
	} else {
		bt.port = defaultMySQLPort
	}

	return nil
}

// roundF2I is a function that returns a rounded int64 from a float64
func roundF2I(val float64, roundOn float64) (newVal int64) {
	var round float64

	digit := val
	_, div := math.Modf(digit)
	if div >= roundOn {
		round = math.Ceil(digit)
	} else {
		round = math.Floor(digit)
	}

	return int64(round)
}

// Setup is a function to setup all beat config & info into the beat struct
func (bt *Mysqlbeat) Setup(b *beat.Beat) error {
	bt.Beat = b
	bt.done = make(chan struct{})
	return nil
}

// Run is a functions that runs the beat
func (bt *Mysqlbeat) Run(b *beat.Beat) error {
	logp.Info("mysqlbeat is running! Hit CTRL-C to stop it.")

	ticker := time.NewTicker(bt.period)
	for {
		select {
		case <-bt.done:
			return nil
		case <-ticker.C:
		}

		err := bt.beat(b)
		if err != nil {
			return err
		}
	}
}

// readData is a function that connects to the mysql, runs the query and returns the data
func (bt *Mysqlbeat) beat(b *beat.Beat) error {
	var err error
	connString := fmt.Sprintf("%v:%v@tcp(%v:%d)/", bt.username, bt.password,
		bt.hostname, bt.port)

	db, err := sql.Open("mysql", connString)
	if err != nil {
		return err
	}
	defer db.Close()

	var rows *sql.Rows
	for index, queryStr := range bt.queries {
		logp.Err(queryStr)
		rows, err = db.Query(queryStr)
		if err != nil {
			return err
		}

		columns, err := rows.Columns()
		if err != nil {
			return err
		}

		if bt.querytypes[index] == "show-slave-delay" {
			// Publish only slave delay
			rows.Next()
			event, err := bt.slaveDelay(rows, columns)
			if err != nil {
				logp.Debug("mysqlbeat", "Failed to get slave-delay: %v", err)
				continue
			}
			b.Events.PublishEvent(event)
		} else if bt.querytypes[index] == "single-row" {
			// Publish only ONE row from the selection
			rows.Next()
			event, err := bt.singleRow(rows, columns)
			if err != nil {
				logp.Err("Failed to get data for single-row: %v", err)
				continue
			}
			b.Events.PublishEvent(event)
		} else {
			if bt.querytypes[index] == "two-columns" {
				// Publish everything in one document
				dtNow := time.Now()
				event := common.MapStr{
					"@timestamp": common.Time(dtNow),
					"type":       "two-columns",
				}
				for rows.Next() {
					attr, err := bt.twoColumns(rows, columns, dtNow)
					if err != nil {
						logp.Err("Failed to get data for two-columns: %v", err)
						continue
					}
					for k, v := range attr {
						event[k] = v
					}
				}
				b.Events.PublishEvent(event)
			} else if bt.querytypes[index] == "multiple-rows" {
				// Publish document for each row:column.
				for rows.Next() {
					event, err := bt.multipleRows(rows, columns)
					if err != nil {
						logp.Err("Failed to get data for multiple-rows: %v",
							err)
						continue
					}
					b.Events.PublishEvent(event)
				}
			}
		}

		if err = rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Cleanup is a function that does nothing on this beat :)
func (bt *Mysqlbeat) Cleanup(b *beat.Beat) error {
	return nil
}

// Stop is a function that runs once the beat is stopped
func (bt *Mysqlbeat) Stop() {
	close(bt.done)
}

func (bt *Mysqlbeat) singleRow(rows *sql.Rows, columns []string) (common.MapStr, error) {
	dtNow := time.Now()
	event := common.MapStr{
		"@timestamp": common.Time(dtNow),
		"type":       "single-row",
	}

	values := make([]sql.RawBytes, len(columns))
	scanArgs := make([]interface{}, len(values))

	for i := range values {
		scanArgs[i] = &values[i]
	}

	err := rows.Scan(scanArgs...)
	if err != nil {
		return nil, err
	}

	for i, col := range values {
		strColName := string(columns[i])
		strColValue := string(col)
		strColType := "string"

		nColValue, err := strconv.ParseInt(strColValue, 0, 64)
		if err == nil {
			strColType = "int"
		}

		fColValue, err := strconv.ParseFloat(strColValue, 64)
		if err == nil {
			if strColType == "string" {
				strColType = "float"
			}
		}

		if strings.HasSuffix(strColName, bt.deltawildcard) {
			var exists bool
			_, exists = bt.oldvalues[strColName]
			if !exists {
				bt.oldvaluesage[strColName] = dtNow

				if strColType == "string" {
					bt.oldvalues[strColName] = strColValue
				} else if strColType == "int" {
					bt.oldvalues[strColName] = nColValue
				} else if strColType == "float" {
					bt.oldvalues[strColName] = fColValue
				}
			} else {
				if dtOld, ok := bt.oldvaluesage[strColName].(time.Time); ok {
					delta := dtNow.Sub(dtOld)

					if strColType == "int" {
						var calcVal int64

						oldVal, _ := bt.oldvalues[strColName].(int64)
						if nColValue > oldVal {
							var devRes float64
							devRes = float64((nColValue - oldVal)) / float64(delta.Seconds())
							calcVal = roundF2I(devRes, .5)
						} else {
							calcVal = 0
						}

						event[strColName] = calcVal

						bt.oldvalues[strColName] = nColValue
						bt.oldvaluesage[strColName] = dtNow
					} else if strColType == "float" {
						var calcVal float64

						oldVal, _ := bt.oldvalues[strColName].(float64)
						if fColValue > oldVal {
							calcVal = (fColValue - oldVal) / float64(delta.Seconds())
						} else {
							calcVal = 0
						}

						event[strColName] = calcVal

						bt.oldvalues[strColName] = fColValue
						bt.oldvaluesage[strColName] = dtNow
					} else {
						event[strColName] = strColValue
					}
				}
			}
		} else {
			if strColType == "string" {
				event[strColName] = strColValue
			} else if strColType == "int" {
				event[strColName] = nColValue
			} else if strColType == "float" {
				event[strColName] = fColValue
			}
		}

	}
	return event, nil
}

func (bt *Mysqlbeat) slaveDelay(rows *sql.Rows, columns []string) (common.MapStr, error) {
	event := common.MapStr{
		"@timestamp": common.Time(time.Now()),
		"type":       "slave-delay",
	}

	values := make([]sql.RawBytes, len(columns))
	scanArgs := make([]interface{}, len(values))

	for i := range values {
		scanArgs[i] = &values[i]
	}

	err := rows.Scan(scanArgs...)
	if err != nil {
		return nil, err
	}

	for i, col := range values {
		if string(columns[i]) == "Seconds_Behind_Master" {
			strColName := string(columns[i])
			strColValue := string(col)

			nColValue, err := strconv.ParseInt(strColValue, 0, 64)

			if err == nil {
				event[strColName] = nColValue
			} else {
				logp.Debug("mysqlbeat", "Error in slaveDelay: %v", err)
			}
		}
	}
	return event, nil
}

func (bt *Mysqlbeat) multipleRows(rows *sql.Rows, columns []string) (common.MapStr, error) {
	mevent := common.MapStr{
		"@timestamp": common.Time(time.Now()),
		"type":       "multiple-rows",
	}

	values := make([]sql.RawBytes, len(columns))
	scanArgs := make([]interface{}, len(values))

	for i := range values {
		scanArgs[i] = &values[i]
	}

	err := rows.Scan(scanArgs...)
	if err != nil {
		return nil, err
	}

	for i, col := range values {
		strColValue := string(col)
		n, err := strconv.ParseInt(strColValue, 0, 64)

		if err == nil {
			mevent[columns[i]] = n
		} else {
			f, err := strconv.ParseFloat(strColValue, 64)

			if err == nil {
				mevent[columns[i]] = f
			} else {
				mevent[columns[i]] = strColValue
			}
		}
	}
	return mevent, nil
}

func (bt *Mysqlbeat) twoColumns(rows *sql.Rows, columns []string, dtNow time.Time) (common.MapStr, error) {
	event := common.MapStr{}
	values := make([]sql.RawBytes, len(columns))
	scanArgs := make([]interface{}, len(values))

	for i := range values {
		scanArgs[i] = &values[i]
	}

	err := rows.Scan(scanArgs...)
	if err != nil {
		return nil, err
	}

	strColName := string(values[0])
	strColValue := string(values[1])
	strColType := "string"

	nColValue, err := strconv.ParseInt(strColValue, 0, 64)
	if err == nil {
		strColType = "int"
	}

	fColValue, err := strconv.ParseFloat(strColValue, 64)
	if err == nil {
		if strColType == "string" {
			strColType = "float"
		}
	}

	if strings.HasSuffix(strColName, bt.deltawildcard) {
		var exists bool
		_, exists = bt.oldvalues[strColName]

		if !exists {
			bt.oldvaluesage[strColName] = dtNow
			if strColType == "string" {
				bt.oldvalues[strColName] = strColValue
			} else if strColType == "int" {
				bt.oldvalues[strColName] = nColValue
			} else if strColType == "float" {
				bt.oldvalues[strColName] = fColValue
			}
		} else {
			if dtOld, ok := bt.oldvaluesage[strColName].(time.Time); ok {
				delta := dtNow.Sub(dtOld)
				if strColType == "int" {
					var calcVal int64

					oldVal, _ := bt.oldvalues[strColName].(int64)
					if nColValue > oldVal {
						var devRes float64
						devRes = float64((nColValue - oldVal)) / float64(delta.Seconds())
						calcVal = roundF2I(devRes, .5)

					} else {
						calcVal = 0
					}

					event[strColName] = calcVal

					bt.oldvalues[strColName] = nColValue
					bt.oldvaluesage[strColName] = dtNow
				} else if strColType == "float" {
					var calcVal float64

					oldVal, _ := bt.oldvalues[strColName].(float64)
					if fColValue > oldVal {
						calcVal = (fColValue - oldVal) / float64(delta.Seconds())
					} else {
						calcVal = 0
					}

					event[strColName] = calcVal

					bt.oldvalues[strColName] = fColValue
					bt.oldvaluesage[strColName] = dtNow
				} else {
					event[strColName] = strColValue
				}
			}
		}
	} else {
		if strColType == "string" {
			event[strColName] = strColValue
		} else if strColType == "int" {
			event[strColName] = nColValue
		} else if strColType == "float" {
			event[strColName] = fColValue
		}
	}
	return event, nil
}
