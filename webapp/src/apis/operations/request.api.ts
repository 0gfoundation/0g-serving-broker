import { get } from "../utils";
import { ModelRequestList } from "../models";

const getHeaders = () => ({});

export const listRequest = async (): Promise<ModelRequestList> =>
  get(`/v1/request`, {
    headers: getHeaders(),
  }).then((res) => res.json());
