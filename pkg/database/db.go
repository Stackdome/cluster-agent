package database

import (
	"database/sql"
	"fmt"
)

type DatabaseConnection interface {
	Ping(connectionString string) error
}

type dialect string

const (
	// PostgresDialect is the dialect for PostgreSQL
	PostgresDialect dialect = "postgres"
)

type databaseConnection struct {
	dialect dialect
}

func NewDatabaseConnection(dialect dialect) *databaseConnection {
	return &databaseConnection{
		dialect: dialect,
	}
}

func (db *databaseConnection) Connect(connectionString string) (*sql.DB, error) {
	// Here you would implement the logic to connect to the database
	// using the provided connection string and dialect.
	// This is a placeholder implementation.
	switch db.dialect {
	case PostgresDialect:
		dbConn, err := sql.Open("postgres", connectionString)
		if err != nil {
			return nil, err
		}
		return dbConn, nil
	default:
		return nil, fmt.Errorf("unsupported database dialect: %s", db.dialect)
	}
}

func (db *databaseConnection) Ping(connectionString string) error {
	dbConn, err := db.Connect(connectionString)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer dbConn.Close()

	// Ping the database to check if the connection is alive
	if err := dbConn.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}
	return nil
}
