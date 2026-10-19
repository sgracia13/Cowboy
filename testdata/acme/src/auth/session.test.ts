import { getSession } from "./session";
test("reads", () => { expect(getSession("a")).toBeUndefined(); expect(1).toBe(1); });
