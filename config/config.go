// Config is put into a different package to prevent cyclic imports in case
// it is needed in several locations

package config

type Config struct {
	Mysqlbeat MysqlbeatConfig
}

type MysqlbeatConfig struct {
	Period        *int64   `yaml:"period"`
	Hostname      string   `yaml:"hostname"`
	Port          string   `yaml:"port"`
	Username      string   `yaml:"username"`
	Password      *string  `yaml:"password"`
	Queries       []string `yaml:"queries"`
	QueryTypes    []string `yaml:"querytypes"`
	DeltaWildCard string   `yaml:"deltawildcard"`
}
