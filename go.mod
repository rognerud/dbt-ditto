module github.com/rognerud/dbt-ditto

go 1.24

// The go directive stays at 1.24 so consumers on that language version can
// still build this module. The toolchain floor is separate: go1.25.7 and
// earlier ship the os.Root FileInfo escape (GO-2026-4602), which canon.Dir
// reaches through filepath.WalkDir, so CI and local builds must use 1.25.8+.
toolchain go1.25.8

require gopkg.in/yaml.v3 v3.0.1

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/cucumber/godog v0.16.0
)

require (
	github.com/cucumber/gherkin/go/v42 v42.0.0 // indirect
	github.com/cucumber/messages/go/v34 v34.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-memdb v1.3.5 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
)
