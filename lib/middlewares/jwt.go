package middlewares

import (
	"time"

	jwt "github.com/appleboy/gin-jwt"
	"github.com/gin-gonic/gin"
	"github.com/sdslabs/SWS/configs"
	"github.com/sdslabs/SWS/lib/mongo"
	"github.com/sdslabs/SWS/lib/utils"
	gojwt "gopkg.in/dgrijalva/jwt-go.v3"
)

const (
	emailKey    = "email"
	usernameKey = "username"
	passwordKey = "password"
	isAdminKey  = "is_admin"
)

// User to store user data after extracting from JWT Claims
type User struct {
	Email    string
	Username string
	IsAdmin  bool
}

type authBody struct {
	Email    string `form:"email" json:"email" binding:"required"`
	Password string `form:"password" json:"password" binding:"required"`
}

type registerBody struct {
	Username string `form:"username" json:"username" binding:"required" valid:"required~Field 'username' is required but was not provided"`
	Password string `form:"password" json:"password" binding:"required" valid:"required~Field 'password' is required but was not provided"`
	Email    string `form:"email" json:"email" binding:"required" valid:"required~Field 'email' is required but was not provided,email"`
}

// RegisterValidator validates the user registration request
func RegisterValidator(ctx *gin.Context) {
	ValidateRequestBody(ctx, &registerBody{})
}

// Register handles registration of new users
func Register(ctx *gin.Context) {
	var register registerBody
	ctx.BindJSON(&register)
	filter := map[string]interface{}{emailKey: register.Email}
	userInfo := mongo.FetchUserInfo(filter)
	if len(userInfo) > 0 {
		ctx.JSON(400, gin.H{
			"error": "email already registered",
		})
		return
	}
	hashedPass, err := utils.HashPassword(register.Password)
	if err != nil {
		ctx.JSON(500, gin.H{
			"error": err,
		})
		return
	}
	createUser := map[string]interface{}{
		emailKey:    register.Email,
		usernameKey: register.Username,
		passwordKey: hashedPass,
		isAdminKey:  false,
	}
	_, err = mongo.RegisterUser(createUser)
	if err != nil {
		ctx.JSON(500, gin.H{
			"error": err,
		})
		return
	}
	ctx.JSON(200, gin.H{
		"message": "user created",
	})
}

// JWT handles the auth through JWT token
var JWT = &jwt.GinJWTMiddleware{
	Realm:         "SDS Gasper",
	Key:           []byte(configs.SWSConfig["secret"].(string)),
	Timeout:       time.Hour,
	MaxRefresh:    time.Hour,
	TokenLookup:   "header: Authorization, query: token, cookie: jwt",
	TokenHeadName: "Bearer",
	TimeFunc:      time.Now,
	Authenticator: func(ctx *gin.Context) (interface{}, error) {
		var auth authBody
		if err := ctx.ShouldBind(&auth); err != nil {
			return nil, jwt.ErrMissingLoginValues
		}
		email := auth.Email
		password := auth.Password
		filter := map[string]interface{}{emailKey: email}
		userInfo := mongo.FetchUserInfo(filter)
		var userData map[string]interface{}
		if len(userInfo) == 0 {
			return nil, jwt.ErrFailedAuthentication
		}
		userData = userInfo[0]
		hashedPassword := userData[passwordKey].(string)
		if !utils.CompareHashWithPassword(hashedPassword, password) {
			return nil, jwt.ErrFailedAuthentication
		}
		return &User{
			Email:    userData[emailKey].(string),
			Username: userData[usernameKey].(string),
			IsAdmin:  userData[isAdminKey].(bool),
		}, nil
	},
	PayloadFunc: func(data interface{}) jwt.MapClaims {
		if v, ok := data.(*User); ok {
			return jwt.MapClaims{
				emailKey:    v.Email,
				usernameKey: v.Username,
				isAdminKey:  v.IsAdmin,
			}
		}
		return jwt.MapClaims{}
	},
	IdentityHandler: func(mapClaims gojwt.MapClaims) interface{} {
		return &User{
			Email:    mapClaims[emailKey].(string),
			Username: mapClaims[usernameKey].(string),
			IsAdmin:  mapClaims[isAdminKey].(bool),
		}
	},
	Authorizator: func(data interface{}, ctx *gin.Context) bool {
		_, ok := data.(*User)
		return ok
	},
	Unauthorized: func(ctx *gin.Context, code int, message string) {
		ctx.JSON(code, gin.H{
			"error": message,
		})
	},
}

// ExtractClaims takes the gin context and returns the User
func ExtractClaims(ctx *gin.Context) *User {
	claimsMap := jwt.ExtractClaims(ctx)
	getUser := JWT.IdentityHandler
	return getUser(claimsMap).(*User)
}

func init() {
	// This keeps the middleware in check if the configuration is correct
	// Prevents runtime errors
	if err := JWT.MiddlewareInit(); err != nil {
		panic(err)
	}
}