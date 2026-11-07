# NexusToken 拼车额度接口

已核对官网公开前端（2026-11-07）：

- [API 调用实现](https://ai.freehyy.com/assets/index-n4YEyM1k.js)
- [重置结果处理](https://ai.freehyy.com/assets/OpenAIQuotaResetCell.vue_vue_type_script_setup_true_lang-DHoK2U8n.js)

网站 API 使用网站登录返回的 `access_token`，并携带 `X-User-UI-Request: 1`。
推理用的 API Key 不能代替网站令牌。默认推理地址 `https://intl.freehyy.com`
对应的网站地址为 `https://ai.freehyy.com`；自定义部署保留原地址及路径前缀。

| 操作 | 方法 | 路径（相对于 `/api/v1`） |
| --- | --- | --- |
| 查询自己的拼车资产及座位 | GET | `/carpool/my/assets` |
| 查询 Key 与座位的绑定 | GET | `/carpool/my/api-key-bindings` |
| 查询个人及整车用量 | GET | `/carpool/my/seats/{seat_id}/usage` |
| 同步额度、重置卡及点数 | POST | `/carpool/my/seats/{seat_id}/openai-quota/refresh` |
| 使用重置卡 | POST | `/carpool/my/seats/{seat_id}/openai-quota/reset` |

重置调用不传请求体，例如：

```sh
curl -X POST 'https://ai.freehyy.com/api/v1/carpool/my/seats/9/openai-quota/reset' \
  -H 'Authorization: Bearer <网站 access_token>' \
  -H 'X-User-UI-Request: 1' \
  -H 'Content-Type: application/json'
```

成功响应使用 `{ "code": 0, "data": { "windows_reset": 2 } }` 结构，
其中 `windows_reset` 是实际重置的窗口数量，不能假定固定为 2。
是否符合重置条件及冷却时间由上游校验。

GPT-Load 通过现有的
`POST /api/groups/{group_id}/credentials/{credential_id}/reset-credits/consume`
管理接口调用上述操作。该接口使用 GPT-Load 管理认证及 `Idempotency-Key`，
无需前端接触网站令牌；后端校验绑定、轮换过期令牌并在成功后同步用量与卡数。
官网公开调用未提供上游幂等参数，因此本地已发送但结果不明的操作不会再次发送，
也不会通过更换本地请求键重复扣卡。
