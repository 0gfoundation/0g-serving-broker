import { del, get, post, put } from "../utils";
import { ModelProvider, ModelProviderList, ModelRefund } from "../models";

const getHeaders = () => ({});

export const listProviderAccount = async (): Promise<ModelProviderList> =>
  get(`/v1/provider`, {
    headers: getHeaders(),
  }).then((res) => res.json());

export const getProviderAccount = (provider: string) =>
  get(`/v1/provider/${provider}`, {
    headers: getHeaders(),
  }).then((res) => res.json());

export const addProviderAccount = (body: ModelProvider) =>
  post(`/v1/provider`, {
    headers: getHeaders(),
    body: body,
  });

export const charge = (provider: string, body: ModelProvider) =>
  post(`/v1/provider/${provider}`, {
    headers: getHeaders(),
    body: body,
  });

export const refund = (provider: string, body: ModelRefund) =>
  post(`/v1/provider/${provider}`, {
    headers: getHeaders(),
    body: body,
  });

export const syncProviderAccounts = () =>
  post(`/v1/sync`, {
    headers: getHeaders(),
  });

export const SyncProviderAccount = (provider: string) =>
  post(`/v1/provider/${provider}/sync`, {
    headers: getHeaders(),
  });
