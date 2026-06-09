package repository

import "errors"

var (
	ErrNotFound      = errors.New("upstream not found")
	ErrAlreadyExists = errors.New("upstream already exists")
)
