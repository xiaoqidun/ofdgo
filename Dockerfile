# 基础镜像
FROM alpine:3.24.1

# 作者信息
LABEL authors="肖其顿 (XIAO QI DUN)"

# 工作路径
WORKDIR /usr/src/app

# 复制程序
COPY main ./main

# 启动命令
ENTRYPOINT [ "/usr/src/app/main","-listen",":80"]