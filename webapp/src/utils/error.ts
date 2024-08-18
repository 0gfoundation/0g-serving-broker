import { message } from "antd";
import { parseJSON } from "./json";

export const alertError = (
  msg:
    | string
    | Response
    | {
        errorFields: {
          errors: string[];
        }[];
      }
) => {
  if (typeof msg === "string") {
    message.error(msg);
    return;
  }

  if (msg instanceof Response) {
    msg.text().then((text) => {
      message.error(parseJSON(text)?.message || text);
    });
    return;
  }

  message.error(msg.errorFields[0].errors[0]);
};
