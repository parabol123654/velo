//go:build race

package db

// raceEnabled is true under go test -race, which makes code several times slower.
const raceEnabled = true
