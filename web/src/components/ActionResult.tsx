import { App } from "antd";
import { ApiError } from "../lib/types";

export function useActionRunner() {
  const { message } = App.useApp();

  return async function runAction(success: string, action: () => Promise<void>) {
    try {
      await action();
      message.success(success);
    } catch (error) {
      if (error instanceof ApiError) {
        message.error(`${error.status} ${error.code}: ${error.message}${error.requestId ? ` (${error.requestId})` : ""}`);
      } else if (error instanceof Error) {
        message.error(error.message);
      } else {
        message.error("未知错误");
      }
    }
  };
}
