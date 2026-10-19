package shortcuts

import (
	"testing"

	"github.com/sgracia13/Cowboy/internal/diff"
)

func kinds(fs []Flag) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Kind] = true
	}
	return m
}

func TestFlags(t *testing.T) {
	src := diff.File{Path: "app/x.py", Status: "modified", Added: []string{"x = 1"}}
	cases := []struct {
		name  string
		files []diff.File
		want  string // "" = no flag expected
	}{
		{"py skip", []diff.File{src, {Path: "tests/test_x.py", Added: []string{"@pytest.mark.skip(reason='flaky')"}}}, "skips"},
		{"js skip", []diff.File{{Path: "src/a.test.ts", Added: []string{"it.skip('works', () => {})"}}}, "skips"},
		{"go skip", []diff.File{{Path: "pkg/a_test.go", Added: []string{"\tt.Skip(\"later\")"}}}, "skips"},
		{"broad except", []diff.File{{Path: "app/y.py", Added: []string{"    except Exception:", "        pass"}}}, "catch_all"},
		{"empty catch", []diff.File{{Path: "src/y.ts", Added: []string{"} catch (e) {}"}}}, "catch_all"},
		{"broad except that logs is fine", []diff.File{{Path: "app/y.py", Added: []string{"    except Exception as e:", "        logger.exception(e)"}}}, ""},
		{"broad except that re-raises is fine", []diff.File{{Path: "app/y.py", Added: []string{"    except Exception:", "        raise"}}}, ""},
		{"broad except returning None", []diff.File{{Path: "app/y.py", Added: []string{"    except Exception:", "", "        return None"}}}, "catch_all"},
		{"one-line except pass", []diff.File{{Path: "app/y.py", Added: []string{"    except (Exception): pass"}}}, "catch_all"},
		{"bare except", []diff.File{{Path: "app/y.py", Added: []string{"    except:", "        log()"}}}, "catch_all"},
		{"except whose body wasn't added", []diff.File{{Path: "app/y.py", Added: []string{"    except Exception:"}}}, ""},
		{"js catch block closed at once", []diff.File{{Path: "src/y.ts", Added: []string{"} catch (e) {", "  // ignore", "}"}}}, "catch_all"},
		{"js catch that handles", []diff.File{{Path: "src/y.ts", Added: []string{"} catch (e) {", "  report(e)", "}"}}}, ""},
		{"java catch returning null", []diff.File{{Path: "src/A.java", Added: []string{"} catch (Exception e) {", "    return null;", "}"}}}, "catch_all"},
		{"java catch that logs", []diff.File{{Path: "src/A.java", Added: []string{"} catch (Exception e) {", "    log.warn(\"x\", e);", "}"}}}, ""},
		{"narrow except is fine", []diff.File{{Path: "app/y.py", Added: []string{"    except ValueError as e:", "        log(e)"}}}, ""},
		{"eslint-disable", []diff.File{{Path: "src/z.ts", Added: []string{"// eslint-disable-next-line"}}}, "suppressions"},
		{"type ignore", []diff.File{{Path: "app/z.py", Added: []string{"x = f()  # type: ignore"}}}, "suppressions"},
		{"old suppression untouched", []diff.File{{Path: "app/z.py", Removed: []string{"x = f()  # type: ignore"}, Added: []string{"y = 2"}}}, ""},
		{"special case comment", []diff.File{{Path: "app/w.py", Added: []string{"    return 0  # special case for test_total"}}}, "special_case_comments"},
		{"asserts removed", []diff.File{src, {Path: "tests/test_x.py", Removed: []string{"    assert f() == 1", "    assert g() == 2"}, Added: []string{"    pass"}}}, "tests_removed"},
		{"test deleted", []diff.File{src, {Path: "tests/test_x.py", Status: "removed", Removed: []string{"def test_a(): assert 1"}}}, "tests_removed"},
		{"asserts moved to another test file", []diff.File{src,
			{Path: "tests/test_x.py", Removed: []string{"    assert f() == 1", "    assert g() == 2"}},
			{Path: "tests/test_y.py", Status: "added", Added: []string{"    assert f() == 1", "    assert g() == 2"}}}, ""},
		{"feature removed with its tests", []diff.File{
			{Path: "app/old.py", Status: "removed", Removed: []string{"def old(): pass"}},
			{Path: "app/x.py", Added: []string{"x = 1"}},
			{Path: "tests/test_old.py", Status: "removed", Removed: []string{"def test_old(): assert old() is None"}}}, ""},
		{"code mostly deleted", []diff.File{
			{Path: "app/x.py", Added: []string{"a"}, Removed: []string{"b", "c", "d", "e"}},
			{Path: "tests/test_x.py", Removed: []string{"    assert x() == 1"}}}, ""},
		{"test-only change keeps asserts optional", []diff.File{{Path: "tests/test_x.py", Removed: []string{"    assert f() == 1"}, Added: []string{"    assert f() == 2"}}}, ""},
		{"ci || true", []diff.File{{Path: ".github/workflows/ci.yml", Removed: []string{"      - run: npm test"}, Added: []string{"      - run: npm test || true"}}}, "ci_loosened"},
		{"ci step removed", []diff.File{{Path: ".gitlab-ci.yml", Removed: []string{"  script: make test"}}}, "ci_loosened"},
	}
	for _, c := range cases {
		got := kinds(Find(c.files, nil))
		if c.want == "" && len(got) > 0 {
			t.Errorf("%s: expected no flags, got %v", c.name, got)
		}
		if c.want != "" && !got[c.want] {
			t.Errorf("%s: expected %s, got %v", c.name, c.want, got)
		}
	}
}

func TestSwitchOff(t *testing.T) {
	files := []diff.File{{Path: "src/z.ts", Added: []string{"// eslint-disable-next-line"}}}
	if got := Find(files, map[string]bool{"suppressions": false}); len(got) != 0 {
		t.Errorf("flag should be off, got %v", got)
	}
}
