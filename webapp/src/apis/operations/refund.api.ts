import { post, get } from "../utils";
import { ModelRefund, ModelRefundList } from "../models";

const getHeaders = () => ({});

export const refund = (provider: string, body: ModelRefund) =>
  post(`/v1/provider/${provider}`, {
    headers: getHeaders(),
    body: body,
  });

export const listRefund = async (): Promise<ModelRefundList> =>
  get(`/v1/refund`, {
    headers: getHeaders(),
  }).then((res) => res.json());
