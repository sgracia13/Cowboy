import { send } from "../webhooks/sender";
export const j2 = () => send("/x", "2");
