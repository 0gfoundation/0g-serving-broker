import { get } from "../utils";
import { ModelServiceList } from "../models";

const getHeaders = () => ({});

export const listService = async (): Promise<ModelServiceList> =>
  get(`/v1/service`, {
    headers: getHeaders(),
  }).then((res) => res.json());
