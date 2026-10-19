package config

import "testing"

func TestSortAllAndPeopleDefaults(t *testing.T) {
	c := Default()
	if !c.SortAllOn() {
		t.Fatal("sort_all should be on by default")
	}
	if c.CommentOnPerson(false) || !c.CommentOnPerson(true) {
		t.Fatal("people get a comment only when careful, by default")
	}
	if err := Merge(&c, []byte(`{"detect":{"sort_all":false},"people":{"comment":"never"}}`)); err != nil {
		t.Fatal(err)
	}
	if c.SortAllOn() {
		t.Fatal("sort_all:false should turn it off")
	}
	if c.CommentOnPerson(true) {
		t.Fatal(`people.comment "never" should mean no comment, even when careful`)
	}
	Merge(&c, []byte(`{"people":{"comment":"always"}}`))
	if !c.CommentOnPerson(false) {
		t.Fatal(`people.comment "always" should comment on every pen`)
	}
	Merge(&c, []byte(`{"people":{"comment":"loud"}}`))
	if !c.CommentOnPerson(false) {
		t.Fatal("an unknown value should be ignored")
	}
}
