package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestBindPositionals(t *testing.T) {
	var first, second string
	specs := []positionalSpec{
		requiredPositional(&first, "first"),
		optionalPositional(&second, "second", "default"),
	}

	if err := bindPositionals(specs, []string{"one"}); err != nil {
		t.Fatalf("bindPositionals() error = %v", err)
	}
	if first != "one" || second != "default" {
		t.Fatalf("bound (%q, %q), want (%q, %q)", first, second, "one", "default")
	}

	if err := bindPositionals(specs, []string{"one", "two"}); err != nil {
		t.Fatalf("bindPositionals() error = %v", err)
	}
	if second != "two" {
		t.Fatalf("second = %q, want %q", second, "two")
	}
}

func TestBindPositionalsRejectsInvalidArity(t *testing.T) {
	var value string
	specs := []positionalSpec{requiredPositional(&value, "snapshot")}

	if err := bindPositionals(specs, nil); err == nil || !strings.Contains(err.Error(), "snapshot is required") {
		t.Fatalf("missing positional error = %v", err)
	}
	if err := bindPositionals(specs, []string{"one", "two"}); err == nil || !strings.Contains(err.Error(), "unexpected positional") {
		t.Fatalf("extra positional error = %v", err)
	}
}

func TestBindPositionalsBindsRequiredRemainder(t *testing.T) {
	var values []string
	specs := []positionalSpec{requiredPositionals(&values, "object key")}

	if err := bindPositionals(specs, nil); err == nil {
		t.Fatal("requiredPositionals accepted no values")
	}
	want := []string{"config", "index/latest"}
	if err := bindPositionals(specs, want); err != nil {
		t.Fatalf("bindPositionals() error = %v", err)
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %q, want %q", values, want)
	}
}

func TestBindPositionalsRequiresRemainderLast(t *testing.T) {
	var values []string
	var last string
	specs := []positionalSpec{
		remainingPositionals(&values, "value"),
		requiredPositional(&last, "last"),
	}

	if err := bindPositionals(specs, []string{"one"}); err == nil || !strings.Contains(err.Error(), "must be last") {
		t.Fatalf("repeatable positional ordering error = %v", err)
	}
}
