package yiigo

import (
	"errors"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jinzhu/gorm"
	"github.com/youtube/vitess/go/pools"
	"golang.org/x/net/context"
	"strings"
	"sync"
	"time"
)

type MysqlBase struct {
	TableName string
}

var (
	mysqlReadPool     *pools.ResourcePool
	mysqlWritePool    *pools.ResourcePool
	mysqlReadPoolMux  sync.Mutex
	mysqlWritePoolMux sync.Mutex
)

type Orm struct {
	Db *gorm.DB
}

func (o Orm) Close() {
	err := o.Db.Close()

	if err != nil {
		LogError("mysql connection close error: ", err.Error())
	}
}

func initReadDb() (*gorm.DB, error) {
	host := GetConfigString("mysql", "slaveHost", "localhost")
	port := GetConfigInt("mysql", "slavePort", 3306)
	username := GetConfigString("mysql", "username", "root")
	password := GetConfigString("mysql", "password", "root")
	dbname := GetConfigString("mysql", "dbname", "yiicms")
	charset := GetConfigString("mysql", "charset", "utf8mb4")

	address := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local", username, password, host, port, dbname, charset)
	readDb, err := gorm.Open("mysql", address)

	if err != nil {
		LogError("connect mysql error: ", err.Error())
		return nil, err
	}

	readDb.SingularTable(true)

	debug := GetConfigBool("default", "debug", true)

	if debug {
		readDb.LogMode(true)
	}

	return readDb, err
}

func initWriteDb() (*gorm.DB, error) {
	host := GetConfigString("mysql", "masterHost", "localhost")
	port := GetConfigInt("mysql", "masterPort", 3306)
	username := GetConfigString("mysql", "username", "root")
	password := GetConfigString("mysql", "password", "root")
	dbname := GetConfigString("mysql", "dbname", "yiicms")
	charset := GetConfigString("mysql", "charset", "utf8mb4")

	address := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local", username, password, host, port, dbname, charset)
	writeDb, err := gorm.Open("mysql", address)

	if err != nil {
		LogError("connect mysql error: ", err.Error())
		return nil, err
	}

	writeDb.SingularTable(true)

	debug := GetConfigBool("default", "debug", true)

	if debug {
		writeDb.LogMode(true)
	}

	return writeDb, err
}

func initReadDbPool() {
	if mysqlReadPool == nil || mysqlReadPool.IsClosed() {
		mysqlReadPoolMux.Lock()
		defer mysqlReadPoolMux.Unlock()

		if mysqlReadPool == nil {
			poolMinActive := GetConfigInt("mysql", "poolMinActive", 100)
			poolMaxActive := GetConfigInt("mysql", "poolMaxActive", 200)
			poolIdleTimeout := GetConfigInt("mysql", "poolIdleTimeout", 2000)

			mysqlReadPool = pools.NewResourcePool(func() (pools.Resource, error) {
				readDb, err := initReadDb()
				return Orm{Db: readDb}, err
			}, poolMinActive, poolMaxActive, time.Duration(poolIdleTimeout)*time.Millisecond)
		}
	}
}

func initWriteDbPool() {
	if mysqlWritePool == nil || mysqlWritePool.IsClosed() {
		mysqlWritePoolMux.Lock()
		defer mysqlWritePoolMux.Unlock()

		poolMinActive := GetConfigInt("mysql", "poolMinActive", 100)
		poolMaxActive := GetConfigInt("mysql", "poolMaxActive", 200)
		poolIdleTimeout := GetConfigInt("mysql", "poolIdleTimeout", 2000)

		if mysqlWritePool == nil {
			mysqlWritePool = pools.NewResourcePool(func() (pools.Resource, error) {
				writeDb, err := initWriteDb()
				return Orm{Db: writeDb}, err
			}, poolMinActive, poolMaxActive, time.Duration(poolIdleTimeout)*time.Millisecond)
		}
	}
}

func poolGetReadDb() (pools.Resource, error) {
	initReadDbPool()

	if mysqlReadPool == nil {
		LogError("mysql read db pool is null")
		return nil, errors.New("mysql read db pool is null")
	}

	ctx := context.TODO()
	readOrmResource, err := mysqlReadPool.Get(ctx)

	if err != nil {
		LogError("mysql get read db error: ", err.Error())
		return nil, err
	}

	if readOrmResource == nil {
		LogError("mysql read pool resource is null")
		return nil, errors.New("mysql read pool resource is null")
	}

	orm := readOrmResource.(Orm)

	if orm.Db.Error != nil {
		LogError("mysql read resource connection err: ", orm.Db.Error.Error())

		orm.Close()
		//连接断开，重新打开
		db, connErr := initReadDb()

		if connErr != nil {
			LogError("mysql read db reconnection err: ", connErr.Error())
			return nil, connErr
		} else {
			return Orm{Db: db}, nil
		}
	}

	return readOrmResource, err
}

func poolGetWriteDb() (pools.Resource, error) {
	initWriteDbPool()

	if mysqlWritePool == nil {
		LogError("mysql write db pool is null")
		return nil, errors.New("mysql write db pool is null")
	}

	ctx := context.TODO()
	writeOrmResource, err := mysqlWritePool.Get(ctx)

	if err != nil {
		LogError("mysql get write db error: ", err.Error())
		return nil, err
	}

	if writeOrmResource == nil {
		LogError("mysql write pool resource is null")
		return nil, errors.New("mysql write pool resource is null")
	}

	orm := writeOrmResource.(Orm)

	if orm.Db.Error != nil {
		LogError("mysql write resource connection err: ", orm.Db.Error.Error())

		orm.Close()
		//连接断开，重新打开
		db, connErr := initWriteDb()

		if connErr != nil {
			LogError("mysql write db reconnection err: ", connErr.Error())
			return nil, connErr
		} else {
			return Orm{Db: db}, nil
		}
	}

	return writeOrmResource, err
}

/**
 * insert 插入
 * data 插入数据 (interface{} 指针)
 */
func (m *MysqlBase) Insert(data interface{}) error {
	dbResource, err := poolGetWriteDb()
	defer mysqlWritePool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	insertErr := db.Table(table).Create(data).Error

	if insertErr != nil {
		LogErrorf("mysql table %s insert error: %s", m.TableName, insertErr.Error())

		return insertErr
	}

	return nil
}

/**
 * update 更新
 * query 查询条件 (map[string]interface{})
 * data 更新字段 (map[string]interface{})
 */
func (m *MysqlBase) Update(query map[string]interface{}, data map[string]interface{}) error {
	dbResource, err := poolGetWriteDb()
	defer mysqlWritePool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	db = db.Table(table)

	db = formatQuery(db, query)

	updateErr := db.Updates(data).Error

	if updateErr != nil {
		LogErrorf("mysql table %s update error: %s", m.TableName, updateErr.Error())

		return updateErr
	}

	return nil
}

/**
 * increment 自增
 * query 查询条件 (map[string]interface{})
 * column 自增字段 (string)
 * inc 增量 (int)
 */
func (m *MysqlBase) Increment(query map[string]interface{}, column string, inc int) error {
	dbResource, err := poolGetWriteDb()
	defer mysqlWritePool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	db = db.Table(table)

	db = formatQuery(db, query)

	expr := fmt.Sprintf("%s + ?", column)
	incErr := db.Update(column, gorm.Expr(expr, inc)).Error

	if incErr != nil {
		LogErrorf("mysql table %s inc error: %s", m.TableName, incErr.Error())

		return incErr
	}

	return nil
}

/**
 * decrement 自减
 * query 查询条件 (map[string]interface{})
 * column 自减字段 (string)
 * dec 减量 (int)
 */
func (m *MysqlBase) Decrement(query map[string]interface{}, column string, dec int) error {
	dbResource, err := poolGetWriteDb()
	defer mysqlWritePool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	db = db.Table(table)

	db = formatQuery(db, query)

	expr := fmt.Sprintf("%s - ?", column)
	decErr := db.Update(column, gorm.Expr(expr, dec)).Error

	if decErr != nil {
		LogErrorf("mysql table %s dec error: %s", m.TableName, decErr.Error())

		return decErr
	}

	return nil
}

/**
 * findOne 查询
 * query 查询条件 (map[string]interface{})
 * data 查询数据 (interface{})
 * fields 查询的字段 ([]string)
 */
func (m *MysqlBase) FindOne(query map[string]interface{}, data interface{}, fields ...[]string) error {
	dbResource, err := poolGetReadDb()
	defer mysqlReadPool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	db = db.Table(table)

	if len(fields) > 0 {
		db = db.Select(fields[0])
	}

	db = formatQuery(db, query)

	findErr := db.First(data).Error

	if findErr != nil {
		errMsg := findErr.Error()

		if errMsg != "record not found" {
			LogErrorf("mysql table %s findone error: %s", m.TableName, errMsg)
		}

		return findErr
	}

	return nil
}

/**
 * find 查询
 * query 查询条件 (map[string]interface{})
 * data 查询数据 (interface{})
 * options (map[string]interface{}) [
 *      fields 查询的字段 ([]string)
 *      count (*int)
 *      order (string)
 *      offset (int)
 *      limit (int)
 * ]
 */
func (m *MysqlBase) Find(query map[string]interface{}, data interface{}, options ...map[string]interface{}) error {
	dbResource, err := poolGetReadDb()
	defer mysqlReadPool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	db = db.Table(table)

	if len(options) > 0 {
		if fields, ok := options[0]["fields"]; ok {
			db = db.Select(fields)
		}

		db = formatQuery(db, query)

		if count, ok := options[0]["count"]; ok {
			db = db.Count(count)
		}

		if ord, ok := options[0]["order"]; ok {
			if order, ok := ord.(string); ok {
				db = db.Order(order)
			}
		}

		if off, ok := options[0]["offset"]; ok {
			if offset, ok := off.(int); ok {
				db = db.Offset(offset)
			}
		}

		if lmt, ok := options[0]["limit"]; ok {
			if limit, ok := lmt.(int); ok {
				db = db.Limit(limit)
			}
		}
	} else {
		db = formatQuery(db, query)
	}

	findErr := db.Find(data).Error

	if findErr != nil {
		errMsg := findErr.Error()

		if errMsg != "record not found" {
			LogErrorf("mysql table %s find error: %s", m.TableName, errMsg)
		}

		return findErr
	}

	return nil
}

/**
 * findOneBySql 查询
 * query 查询条件 (map[string]interface{}) [
 *      sql SQL查询语句 (string)
 *      fields 查询的字段 ([]string)
 * ]
 * data 查询数据 (interface{})
 * bindParams SQL语句中 "?" 绑定的值
 */
func (m *MysqlBase) FindOneBySql(query map[string]interface{}, data interface{}, bindParams ...interface{}) error {
	dbResource, err := poolGetReadDb()
	defer mysqlReadPool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	db = db.Table(table)

	if fields, ok := query["fields"]; ok {
		db = db.Select(fields)
	}

	if sql, ok := query["sql"]; ok {
		db = db.Where(sql, bindParams...)
	}

	findErr := db.First(data).Error

	if findErr != nil {
		errMsg := findErr.Error()

		if errMsg != "record not found" {
			LogErrorf("mysql table %s findone error: %s", m.TableName, errMsg)
		}

		return findErr
	}

	return nil
}

/**
 * findBySql 查询
 * query 查询条件 (map[string]interface{}) [
 *      sql SQL查询语句 (string)
 *      fields 查询的字段 ([]string)
 *      count (*int)
 *      order (string)
 *      offset (int)
 *      limit (int)
 * ]
 * data 查询数据 (interface{})
 * bindParams SQL语句中 "?" 绑定的值
 */
func (m *MysqlBase) FindBySql(query map[string]interface{}, data interface{}, bindParams ...interface{}) error {
	dbResource, err := poolGetReadDb()
	defer mysqlReadPool.Put(dbResource)

	if err != nil {
		return err
	}

	db := dbResource.(Orm).Db

	if m.TableName == "" {
		tableErr := errors.New("tablename empty")
		LogError("init db error: tablename empty")

		return tableErr
	}

	var table string
	prefix := GetConfigString("mysql", "prefix", "")

	if prefix != "" {
		table = prefix + m.TableName
	} else {
		table = m.TableName
	}

	db = db.Table(table)

	if fields, ok := query["fields"]; ok {
		db = db.Select(fields)
	}

	if sql, ok := query["sql"]; ok {
		db = db.Where(sql, bindParams...)
	}

	if count, ok := query["count"]; ok {
		db = db.Count(count)
	}

	if ord, ok := query["order"]; ok {
		if order, ok := ord.(string); ok {
			db = db.Order(order)
		}
	}

	if off, ok := query["offset"]; ok {
		if offset, ok := off.(int); ok {
			db = db.Offset(offset)
		}
	}

	if lmt, ok := query["limit"]; ok {
		if limit, ok := lmt.(int); ok {
			db = db.Limit(limit)
		}
	}

	findErr := db.Find(data).Error

	if findErr != nil {
		errMsg := findErr.Error()

		if errMsg != "record not found" {
			LogErrorf("mysql table %s find error: %s", m.TableName, errMsg)
		}

		return findErr
	}

	return nil
}

func formatQuery(db *gorm.DB, query map[string]interface{}) *gorm.DB {
	if len(query) > 0 {
		for key, value := range query {
			tmp := strings.Split(key, ":")

			if len(tmp) == 2 {
				switch tmp[1] {
				case "eq":
					query := fmt.Sprintf("%s = ?", tmp[0])
					db = db.Where(query, value)
				case "ne":
					query := fmt.Sprintf("%s <> ?", tmp[0])
					db = db.Where(query, value)
				case "ge":
					query := fmt.Sprintf("%s >= ?", tmp[0])
					db = db.Where(query, value)
				case "gt":
					query := fmt.Sprintf("%s > ?", tmp[0])
					db = db.Where(query, value)
				case "le":
					query := fmt.Sprintf("%s <= ?", tmp[0])
					db = db.Where(query, value)
				case "lt":
					query := fmt.Sprintf("%s < ?", tmp[0])
					db = db.Where(query, value)
				case "lk":
					if str, ok := value.(string); ok {
						value = fmt.Sprintf("%%%s%%", str)
						query := fmt.Sprintf("%s LIKE ?", tmp[0])
						db = db.Where(query, value)
					}
				case "in":
					query := fmt.Sprintf("%s in (?)", tmp[0])
					db = db.Where(query, value)
				case "ni":
					db = db.Not(tmp[0], value)
				case "fi":
					query := fmt.Sprintf("find_in_set(?, %s)", tmp[0])
					db = db.Where(query, value)
				}
			} else {
				query := fmt.Sprintf("%s = ?", tmp[0])
				db = db.Where(query, value)
			}
		}
	}

	return db
}